package acpagentbridge

import (
	"context"
	"strings"
)

type acpTaskStreamObservationPhase uint8

const (
	acpTaskStreamObservationIdle acpTaskStreamObservationPhase = iota
	acpTaskStreamObservationResolving
	acpTaskStreamObservationAttached
	acpTaskStreamObservationResuming
	acpTaskStreamObservationClosed
	acpTaskStreamObservationFailedTerminal
)

// acpTaskStreamObservation is the sole phase and generation owner for one
// parent tool call. Its mutation methods are called only while the mux lock is
// held; workers never compare or write its fields directly.
type acpTaskStreamObservation struct {
	phase      acpTaskStreamObservationPhase
	notices    acpTaskStreamNoticeKind
	generation *acpTaskStreamObservationGeneration
}

type acpTaskStreamObservationGeneration struct {
	ctx      context.Context
	cancel   context.CancelFunc
	boundary chan struct{}
}

func (o *acpTaskStreamObservation) claim(parent context.Context) *acpTaskStreamObservationGeneration {
	if o == nil || o.phase != acpTaskStreamObservationIdle {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	generation := &acpTaskStreamObservationGeneration{
		ctx:      ctx,
		cancel:   cancel,
		boundary: make(chan struct{}, 1),
	}
	o.phase = acpTaskStreamObservationResolving
	o.generation = generation
	return generation
}

func (o *acpTaskStreamObservation) owns(generation *acpTaskStreamObservationGeneration) bool {
	return o != nil && generation != nil && o.generation == generation
}

func (o *acpTaskStreamObservation) attach(generation *acpTaskStreamObservationGeneration) bool {
	if !o.owns(generation) || o.phase != acpTaskStreamObservationResolving {
		return false
	}
	o.phase = acpTaskStreamObservationAttached
	return true
}

func (o *acpTaskStreamObservation) beginResume(generation *acpTaskStreamObservationGeneration) bool {
	if !o.owns(generation) || o.phase != acpTaskStreamObservationAttached {
		return false
	}
	o.phase = acpTaskStreamObservationResuming
	return true
}

func (o *acpTaskStreamObservation) finishResume(generation *acpTaskStreamObservationGeneration) bool {
	if !o.owns(generation) || o.phase != acpTaskStreamObservationResuming {
		return false
	}
	o.phase = acpTaskStreamObservationAttached
	return true
}

func (o *acpTaskStreamObservation) closeGeneration(generation *acpTaskStreamObservationGeneration) bool {
	if !o.owns(generation) {
		return false
	}
	o.phase = acpTaskStreamObservationClosed
	return true
}

func (o *acpTaskStreamObservation) closeParent() *acpTaskStreamObservationGeneration {
	if o == nil {
		return nil
	}
	o.phase = acpTaskStreamObservationClosed
	return o.generation
}

func (o *acpTaskStreamObservation) cancelResolving() *acpTaskStreamObservationGeneration {
	if o == nil || o.phase != acpTaskStreamObservationResolving {
		return nil
	}
	o.phase = acpTaskStreamObservationClosed
	return o.generation
}

func (o *acpTaskStreamObservation) retryStopped(
	generation *acpTaskStreamObservationGeneration,
	sealed bool,
) bool {
	if !o.owns(generation) {
		return true
	}
	switch o.phase {
	case acpTaskStreamObservationResolving:
		return sealed
	case acpTaskStreamObservationResuming:
		return false
	default:
		return true
	}
}

func (o *acpTaskStreamObservation) prepareResolveFailure(
	generation *acpTaskStreamObservationGeneration,
	stopped bool,
) bool {
	if !o.owns(generation) || o.phase != acpTaskStreamObservationResolving {
		return false
	}
	if stopped {
		o.phase = acpTaskStreamObservationClosed
		return false
	}
	return true
}

func (o *acpTaskStreamObservation) finishResolveFailure(
	generation *acpTaskStreamObservationGeneration,
	stopped bool,
	retryable bool,
) {
	if !o.owns(generation) || o.phase != acpTaskStreamObservationResolving {
		return
	}
	switch {
	case stopped:
		o.phase = acpTaskStreamObservationClosed
	case retryable:
		// No stream authority was attached. A later canonical anchor may retry
		// after Task registration catches up.
		o.phase = acpTaskStreamObservationIdle
	default:
		o.phase = acpTaskStreamObservationFailedTerminal
	}
}

func (o *acpTaskStreamObservation) failActive(generation *acpTaskStreamObservationGeneration) bool {
	if !o.owns(generation) {
		return false
	}
	switch o.phase {
	case acpTaskStreamObservationAttached, acpTaskStreamObservationResuming:
		o.phase = acpTaskStreamObservationFailedTerminal
		return true
	case acpTaskStreamObservationClosed:
		return false
	default:
		o.phase = acpTaskStreamObservationClosed
		return false
	}
}

func (o *acpTaskStreamObservation) claimNotice(
	generation *acpTaskStreamObservationGeneration,
	kind acpTaskStreamNoticeKind,
) bool {
	if !o.owns(generation) || o.phase == acpTaskStreamObservationClosed || o.notices&kind != 0 {
		return false
	}
	o.notices |= kind
	return true
}

func (o *acpTaskStreamObservation) boundary() chan struct{} {
	if o == nil || o.generation == nil {
		return nil
	}
	return o.generation.boundary
}

func (m *acpTaskStreamMux) observationLocked(callID string) *acpTaskStreamObservation {
	callID = strings.TrimSpace(callID)
	observation := m.observations[callID]
	if observation == nil {
		observation = &acpTaskStreamObservation{phase: acpTaskStreamObservationIdle}
		m.observations[callID] = observation
	}
	return observation
}

func (m *acpTaskStreamMux) claimObservation(callID string) *acpTaskStreamObservationGeneration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealed {
		return nil
	}
	generation := m.observationLocked(callID).claim(m.ctx)
	if generation != nil {
		m.active++
		m.wg.Add(1)
	}
	return generation
}

func (m *acpTaskStreamMux) closeParentObservation(callID string) *acpTaskStreamObservationGeneration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observationLocked(callID).closeParent()
}

func (m *acpTaskStreamMux) updateObservation(
	callID string,
	update func(*acpTaskStreamObservation) bool,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	observation := m.observations[strings.TrimSpace(callID)]
	return observation != nil && update(observation)
}

func (m *acpTaskStreamMux) attachObservation(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	return m.updateObservation(callID, func(observation *acpTaskStreamObservation) bool {
		return observation.attach(generation)
	})
}

func (m *acpTaskStreamMux) beginObservationResume(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	return m.updateObservation(callID, func(observation *acpTaskStreamObservation) bool {
		return observation.beginResume(generation)
	})
}

func (m *acpTaskStreamMux) finishObservationResume(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	return m.updateObservation(callID, func(observation *acpTaskStreamObservation) bool {
		return observation.finishResume(generation)
	})
}

func (m *acpTaskStreamMux) closeObservation(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	return m.updateObservation(callID, func(observation *acpTaskStreamObservation) bool {
		return observation.closeGeneration(generation)
	})
}

func (m *acpTaskStreamMux) observationRetryStopped(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	if generation == nil || generation.ctx.Err() != nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	observation := m.observations[strings.TrimSpace(callID)]
	return observation == nil || observation.retryStopped(generation, m.sealed)
}

func (m *acpTaskStreamMux) prepareResolveFailure(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	stopped := m.sealed || generation == nil || generation.ctx.Err() != nil
	observation := m.observations[strings.TrimSpace(callID)]
	return observation != nil && observation.prepareResolveFailure(generation, stopped)
}

func (m *acpTaskStreamMux) completeResolveFailure(
	callID string,
	generation *acpTaskStreamObservationGeneration,
	retryable bool,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stopped := m.sealed || generation == nil || generation.ctx.Err() != nil
	observation := m.observations[strings.TrimSpace(callID)]
	if observation != nil {
		observation.finishResolveFailure(generation, stopped, retryable)
	}
}

func (m *acpTaskStreamMux) failActiveObservation(
	callID string,
	generation *acpTaskStreamObservationGeneration,
) bool {
	return m.updateObservation(callID, func(observation *acpTaskStreamObservation) bool {
		return observation.failActive(generation)
	})
}

func (m *acpTaskStreamMux) claimObservationNotice(
	callID string,
	generation *acpTaskStreamObservationGeneration,
	kind acpTaskStreamNoticeKind,
) bool {
	return m.updateObservation(callID, func(observation *acpTaskStreamObservation) bool {
		return observation.claimNotice(generation, kind)
	})
}

func (m *acpTaskStreamMux) observationBoundary(callID string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	observation := m.observations[strings.TrimSpace(callID)]
	if observation == nil {
		return nil
	}
	return observation.boundary()
}

func (m *acpTaskStreamMux) cancelResolvingObservations() []*acpTaskStreamObservationGeneration {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealed = true
	generations := make([]*acpTaskStreamObservationGeneration, 0)
	for _, observation := range m.observations {
		if generation := observation.cancelResolving(); generation != nil {
			generations = append(generations, generation)
		}
	}
	return generations
}
