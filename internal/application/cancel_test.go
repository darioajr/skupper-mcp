package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darioajr/skupper-scope/internal/domain"
)

// blockingRepo blocks until its context is cancelled, so cancellation and
// timeout handling can be exercised without a cluster.
type blockingRepo struct {
	entered chan struct{}
	once    bool
}

func (b *blockingRepo) block(ctx context.Context) error {
	if !b.once {
		b.once = true
		close(b.entered)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingRepo) ListSites(ctx context.Context, _ Query) (Result[domain.Site], error) {
	return Result[domain.Site]{}, b.block(ctx)
}

func (b *blockingRepo) ListLinks(ctx context.Context, _ Query) (Result[domain.Link], error) {
	return Result[domain.Link]{}, b.block(ctx)
}

func (b *blockingRepo) ListListeners(ctx context.Context, _ Query) (Result[domain.Listener], error) {
	return Result[domain.Listener]{}, b.block(ctx)
}

func (b *blockingRepo) ListConnectors(ctx context.Context, _ Query) (Result[domain.Connector], error) {
	return Result[domain.Connector]{}, b.block(ctx)
}

func TestListingHonoursContextCancellation(t *testing.T) {
	repo := &blockingRepo{entered: make(chan struct{})}
	svc := newService(t, repo, []string{"west-cluster"}, []string{"west"}, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := svc.ListSites(ctx, "", "")
		done <- err
	}()

	select {
	case <-repo.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the repository was never called")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListSites did not return after its context was cancelled")
	}
}

func TestListingHonoursADeadline(t *testing.T) {
	repo := &blockingRepo{entered: make(chan struct{})}
	svc := newService(t, repo, []string{"west-cluster"}, []string{"west"}, 100)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := svc.ListSites(ctx, "", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("ListSites took %s; the deadline was not honoured", elapsed)
	}
}

// TestListAcrossStopsBetweenScopesOnCancellation checks the loop itself, not
// only the repository call it delegates to.
func TestListAcrossStopsBetweenScopesOnCancellation(t *testing.T) {
	repo := twoSiteNetwork()
	svc := twoSiteService(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.ListSites(ctx, "", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if len(repo.calls) != 0 {
		t.Errorf("the repository was called %d time(s) with an already-cancelled context", len(repo.calls))
	}
}
