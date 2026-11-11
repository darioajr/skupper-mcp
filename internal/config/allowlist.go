package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Allowlist decides which (context, namespace) pairs may be read. It is the
// single authorization gate in front of every Kubernetes call, and it is
// consulted before any I/O happens.
type Allowlist struct {
	contexts   []string
	contextSet map[string]struct{}
	global     []string
	perContext map[string][]string
	defaultCtx string
}

// Errors returned when a request falls outside the allowlist. They are
// deliberately explicit rather than being reported as an empty result: a
// silently empty listing would let a client map the allowlist by inference.
var (
	ErrContextNotAllowed   = errors.New("context not allowed")
	ErrNamespaceNotAllowed = errors.New("namespace not allowed")
)

// Allowlist builds the allowlist from configuration.
//
// Namespace entries are either "namespace", allowed in every allowed context,
// or "context/namespace", allowed only in that context.
func (c Config) Allowlist() (*Allowlist, error) {
	contexts := slices.Clone(c.Kubernetes.AllowedContexts)
	if len(contexts) == 0 {
		contexts = []string{c.Kubernetes.CurrentContextAlias}
	}
	set := make(map[string]struct{}, len(contexts))
	for _, ctx := range contexts {
		if ctx == "" {
			return nil, fmt.Errorf("%w: allowedContexts contains an empty name", ErrInvalidConfig)
		}
		set[ctx] = struct{}{}
	}

	a := &Allowlist{
		contexts:   contexts,
		contextSet: set,
		perContext: map[string][]string{},
		defaultCtx: contexts[0],
	}

	for _, entry := range c.Kubernetes.AllowedNamespaces {
		ctxName, ns, qualified := strings.Cut(entry, "/")
		if !qualified {
			ctxName, ns = "", entry
		}
		if ns == "" || strings.Contains(ns, "/") {
			return nil, fmt.Errorf("%w: allowedNamespaces entry %q is not \"namespace\" or \"context/namespace\"",
				ErrInvalidConfig, entry)
		}
		if ctxName == "" {
			if !slices.Contains(a.global, ns) {
				a.global = append(a.global, ns)
			}
			continue
		}
		if _, ok := set[ctxName]; !ok {
			return nil, fmt.Errorf("%w: allowedNamespaces entry %q names context %q, which is not in allowedContexts",
				ErrInvalidConfig, entry, ctxName)
		}
		if !slices.Contains(a.perContext[ctxName], ns) {
			a.perContext[ctxName] = append(a.perContext[ctxName], ns)
		}
	}
	return a, nil
}

// Contexts returns every allowed context, in configuration order.
func (a *Allowlist) Contexts() []string { return slices.Clone(a.contexts) }

// DefaultContext is used when a client omits the context argument.
func (a *Allowlist) DefaultContext() string { return a.defaultCtx }

// Namespaces returns every namespace allowed in ctx, in configuration order,
// with globally allowed namespaces first.
func (a *Allowlist) Namespaces(ctx string) []string {
	out := slices.Clone(a.global)
	for _, ns := range a.perContext[ctx] {
		if !slices.Contains(out, ns) {
			out = append(out, ns)
		}
	}
	return out
}

// ResolveContext validates a client-supplied context name. An empty name
// selects the default context.
func (a *Allowlist) ResolveContext(name string) (string, error) {
	if name == "" {
		return a.defaultCtx, nil
	}
	if _, ok := a.contextSet[name]; !ok {
		return "", fmt.Errorf("%w: %q is not one of %s", ErrContextNotAllowed, name, quoteList(a.contexts))
	}
	return name, nil
}

// ResolveScope turns a client-supplied (context, namespace) pair into the
// concrete set of namespaces to read. An empty namespace means every namespace
// allowed in that context, never every namespace in the cluster.
func (a *Allowlist) ResolveScope(ctxName, namespace string) (string, []string, error) {
	resolved, err := a.ResolveContext(ctxName)
	if err != nil {
		return "", nil, err
	}
	allowed := a.Namespaces(resolved)
	if len(allowed) == 0 {
		return "", nil, fmt.Errorf("%w: context %q has no allowed namespace", ErrNamespaceNotAllowed, resolved)
	}
	if namespace == "" {
		return resolved, allowed, nil
	}
	if !slices.Contains(allowed, namespace) {
		return "", nil, fmt.Errorf("%w: %q is not allowed in context %q; allowed namespaces are %s",
			ErrNamespaceNotAllowed, namespace, resolved, quoteList(allowed))
	}
	return resolved, []string{namespace}, nil
}

// AllContexts returns every allowed (context, namespaces) pair, for tools that
// operate over the whole visible network.
func (a *Allowlist) AllContexts() map[string][]string {
	out := make(map[string][]string, len(a.contexts))
	for _, ctx := range a.contexts {
		out[ctx] = a.Namespaces(ctx)
	}
	return out
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = `"` + s + `"`
	}
	return strings.Join(quoted, ", ")
}
