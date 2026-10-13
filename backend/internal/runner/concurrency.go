package runner

import "context"

// SetMaxConcurrentTasks updates the limit without interrupting running tasks.
// Lowering it pauses new work until enough running tasks release their slots.
func (m *Manager) SetMaxConcurrentTasks(limit int) {
	m.slotMu.Lock()
	defer m.slotMu.Unlock()
	m.maxConcurrentTasks = limit
	m.notifySlotsChanged()
}

func (m *Manager) acquireSlot(ctx context.Context) error {
	for {
		m.slotMu.Lock()
		if err := ctx.Err(); err != nil {
			m.slotMu.Unlock()
			return err
		}
		if m.usedSlots < m.maxConcurrentTasks {
			m.usedSlots++
			m.slotMu.Unlock()
			return nil
		}
		changed := m.slotsChanged
		m.slotMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (m *Manager) releaseSlot() {
	m.slotMu.Lock()
	defer m.slotMu.Unlock()
	m.usedSlots--
	m.notifySlotsChanged()
}

// Caller holds slotMu.
func (m *Manager) notifySlotsChanged() {
	close(m.slotsChanged)
	m.slotsChanged = make(chan struct{})
}
