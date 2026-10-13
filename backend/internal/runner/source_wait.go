package runner

import "context"

func (m *Manager) waitWithoutSlot(ctx context.Context, held *bool) func(func() error) error {
	return func(wait func() error) error {
		m.releaseSlot()
		*held = false
		if err := wait(); err != nil {
			return err
		}
		if err := m.acquireSlot(ctx); err != nil {
			return err
		}
		*held = true
		return nil
	}
}
