package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
	"github.com/darioajr/skupper-scope/internal/kubernetes/skupperapi"
)

// Group version resources for the four Skupper v2 kinds this server reads.
var (
	sitesGVR      = gvr(skupperapi.ResourceSites)
	linksGVR      = gvr(skupperapi.ResourceLinks)
	listenersGVR  = gvr(skupperapi.ResourceListeners)
	connectorsGVR = gvr(skupperapi.ResourceConnectors)
)

func gvr(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: skupperapi.GroupName, Version: skupperapi.Version, Resource: resource}
}

// Repository reads Skupper resources through the dynamic client. It satisfies
// application.SkupperResourceRepository and exposes no write verb.
type Repository struct {
	factory         *ClientFactory
	pageSize        int64
	timeout         time.Duration
	userCredentials bool
}

var _ application.SkupperResourceRepository = (*Repository)(nil)

// NewRepository validates its dependencies and returns a repository.
func NewRepository(factory *ClientFactory, cfg config.Config) (*Repository, error) {
	if factory == nil {
		return nil, errors.New("kubernetes: client factory is required")
	}
	if cfg.Kubernetes.PageSize <= 0 {
		return nil, errors.New("kubernetes: pageSize must be positive")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("kubernetes: requestTimeout must be positive")
	}
	return &Repository{factory: factory, pageSize: cfg.Kubernetes.PageSize, timeout: cfg.RequestTimeout, userCredentials: cfg.Auth.Enabled}, nil
}

// ListSites implements application.SkupperResourceRepository.
func (r *Repository) ListSites(ctx context.Context, q application.Query) (application.Result[domain.Site], error) {
	return listResource(ctx, r, q, sitesGVR, MapSite)
}

// ListLinks implements application.SkupperResourceRepository.
func (r *Repository) ListLinks(ctx context.Context, q application.Query) (application.Result[domain.Link], error) {
	return listResource(ctx, r, q, linksGVR, MapLink)
}

// ListListeners implements application.SkupperResourceRepository.
func (r *Repository) ListListeners(ctx context.Context, q application.Query) (application.Result[domain.Listener], error) {
	return listResource(ctx, r, q, listenersGVR, MapListener)
}

// ListConnectors implements application.SkupperResourceRepository.
func (r *Repository) ListConnectors(ctx context.Context, q application.Query) (application.Result[domain.Connector], error) {
	return listResource(ctx, r, q, connectorsGVR, MapConnector)
}

// listResource pages through one namespaced resource, stopping as soon as the
// caller's budget is filled so a large namespace cannot be pulled in full.
func listResource[T any](
	ctx context.Context,
	r *Repository,
	q application.Query,
	resource schema.GroupVersionResource,
	mapItem func(string, unstructured.Unstructured) (T, error),
) (application.Result[T], error) {
	var zero application.Result[T]
	if q.Scope.Namespace == "" {
		// Defence in depth: the allowlist already refuses to produce an empty
		// namespace, and the dynamic client would read cluster-wide if one
		// reached it.
		return zero, fmt.Errorf("kubernetes: refusing to list %s cluster-wide; a namespace is required", resource.Resource)
	}
	if q.Limit <= 0 {
		return zero, fmt.Errorf("kubernetes: limit must be positive, got %d", q.Limit)
	}

	var client dynamic.Interface
	var err error
	if r.userCredentials {
		client, err = r.factory.UserClient(ctx, q.Scope.Context)
	} else {
		client, err = r.factory.Client(q.Scope.Context)
	}
	if err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return zero, err
		}
		return zero, fmt.Errorf("%w: %w", application.ErrScopeUnavailable, err)
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	iface := client.Resource(resource).Namespace(q.Scope.Namespace)
	out := application.Result[T]{Items: make([]T, 0, min(int64(q.Limit), r.pageSize))}
	var continueToken string

	for {
		limit := min(int64(q.Limit-len(out.Items))+1, r.pageSize)
		list, err := iface.List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
		if err != nil {
			if r.userCredentials && (apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)) {
				return zero, fmt.Errorf("%w: native Kubernetes access denied for %s/%s", auth.ErrForbidden, q.Scope.Context, q.Scope.Namespace)
			}
			return zero, classifyListError(resource, q.Scope, err)
		}
		for i := range list.Items {
			if len(out.Items) >= q.Limit {
				out.Truncated = true
				return out, nil
			}
			item, err := mapItem(q.Scope.Context, list.Items[i])
			if err != nil {
				return zero, err
			}
			out.Items = append(out.Items, item)
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			return out, nil
		}
		if err := ctx.Err(); err != nil {
			return zero, fmt.Errorf("%w: listing %s in %s/%s: %w",
				application.ErrScopeUnavailable, resource.Resource, q.Scope.Context, q.Scope.Namespace, err)
		}
	}
}

// classifyListError decides whether a failed read should degrade the result
// with a warning or fail the whole tool call.
//
// A missing CRD, a forbidden namespace, or an unreachable API server is a gap
// in one scope of a possibly multi-cluster view: the rest of the answer is
// still worth returning, as long as the gap is reported. Anything else is a
// real error.
func classifyListError(resource schema.GroupVersionResource, scope application.Scope, err error) error {
	base := fmt.Errorf("listing %s in %s/%s: %w", resource.Resource, scope.Context, scope.Namespace, err)
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%w: the %s.%s CRD is not installed in context %q: %w",
			application.ErrScopeUnavailable, resource.Resource, resource.Group, scope.Context, err)
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return fmt.Errorf("%w: this server's credentials may not list %s in %s/%s; "+
			"grant get and list on %s.%s: %w",
			application.ErrScopeUnavailable, resource.Resource, scope.Context, scope.Namespace,
			resource.Resource, resource.Group, err)
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), apierrors.IsServiceUnavailable(err),
		apierrors.IsInternalError(err), apierrors.IsTooManyRequests(err),
		errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", application.ErrScopeUnavailable, base)
	default:
		return base
	}
}
