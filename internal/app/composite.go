// file: internal/app/composite.go

package app

import (
	"context"
	"fmt"
	"sync"

	"rule-router/internal/lifecycle"
)

// Verify CompositeApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*CompositeApp)(nil)

// CompositeApp runs multiple lifecycle.Application instances concurrently
// and manages shared BaseApp resource cleanup.
type CompositeApp struct {
	apps []lifecycle.Application
	base *BaseApp
}

// NewCompositeApp creates a CompositeApp that runs the given apps concurrently.
// BaseApp shared resources are cleaned up once in Close().
func NewCompositeApp(apps []lifecycle.Application, base *BaseApp) *CompositeApp {
	return &CompositeApp{apps: apps, base: base}
}

// Run starts all sub-apps concurrently. If any app returns an error,
// the context is cancelled and all apps shut down.
func (c *CompositeApp) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(c.apps))
	var wg sync.WaitGroup

	for _, a := range c.apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.Run(ctx); err != nil {
				errs <- err
				cancel() // signal other apps to stop
			}
		}()
	}

	wg.Wait()
	close(errs)

	// Return the first error, if any
	for err := range errs {
		return err
	}
	return nil
}

// Close shuts down all sub-apps, then cleans up shared BaseApp resources.
func (c *CompositeApp) Close() error {
	var errs []error
	for _, a := range c.apps {
		if err := a.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := c.base.Close(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("composite close errors: %v", errs)
	}
	return nil
}
