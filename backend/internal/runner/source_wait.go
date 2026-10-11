package runner

import "context"

func (m *Manager) waitWithoutSlot(ctx context.Context, held *bool) func(func() error) error {
	return func(wait func() error) error {
		<-m.sem
		*held = false
		if err := wait(); err != nil {
			return err
		}
		select {
		case m.sem <- struct{}{}:
			*held = true
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
