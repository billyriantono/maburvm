package libvirt

import (
	"context"
	"fmt"
	"log"
	"sync"

	"libvirt.org/go/libvirt"
)

// DomainEventType represents libvirt domain event types
type DomainEventType int

const (
	DomainEventDefined     DomainEventType = libvirt.DOMAIN_EVENT_DEFINED
	DomainEventUndefined   DomainEventType = libvirt.DOMAIN_EVENT_UNDEFINED
	DomainEventStarted     DomainEventType = libvirt.DOMAIN_EVENT_STARTED
	DomainEventSuspended   DomainEventType = libvirt.DOMAIN_EVENT_SUSPENDED
	DomainEventResumed     DomainEventType = libvirt.DOMAIN_EVENT_RESUMED
	DomainEventStopped     DomainEventType = libvirt.DOMAIN_EVENT_STOPPED
	DomainEventShutdown    DomainEventType = libvirt.DOMAIN_EVENT_SHUTDOWN
	DomainEventPMSuspended DomainEventType = libvirt.DOMAIN_EVENT_PMSUSPENDED
	DomainEventCrashed     DomainEventType = libvirt.DOMAIN_EVENT_CRASHED
)

// DomainState represents the state of a domain
type DomainState int

const (
	DomainStateNoState     DomainState = libvirt.DOMAIN_NOSTATE
	DomainStateRunning     DomainState = libvirt.DOMAIN_RUNNING
	DomainStateBlocked     DomainState = libvirt.DOMAIN_BLOCKED
	DomainStatePaused      DomainState = libvirt.DOMAIN_PAUSED
	DomainStateShutdown    DomainState = libvirt.DOMAIN_SHUTDOWN
	DomainStateShutoff     DomainState = libvirt.DOMAIN_SHUTOFF
	DomainStateCrashed     DomainState = libvirt.DOMAIN_CRASHED
	DomainStatePMSuspended DomainState = libvirt.DOMAIN_PMSUSPENDED
)

// DomainEventDetails contains information about a domain event
type DomainEventDetails struct {
	Event   DomainEventType
	Domain  string
	UUID    string
	State   DomainState
	Details string
}

// EventCallback is the function signature for domain event handlers
type EventCallback func(details DomainEventDetails)

// EventManager handles libvirt domain lifecycle events
type EventManager struct {
	mu         sync.RWMutex
	callbacks  map[DomainEventType][]EventCallback
	conn       *libvirt.Connect
	ctx        context.Context
	cancel     context.CancelFunc
	callbackID int
	running    bool
}

var (
	globalEventManager *EventManager
	eventManagerOnce   sync.Once
	eventManagerErr    error
)

// InitializeEventManager initializes the global event manager
func InitializeEventManager() error {
	eventManagerOnce.Do(func() {
		globalEventManager, eventManagerErr = NewEventManager()
	})
	return eventManagerErr
}

// NewEventManager creates a new event manager
func NewEventManager() (*EventManager, error) {
	ctx, cancel := context.WithCancel(context.Background())

	return &EventManager{
		callbacks: make(map[DomainEventType][]EventCallback),
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

// RegisterCallback adds a callback for a specific event type
func (em *EventManager) RegisterCallback(eventType DomainEventType, callback EventCallback) {
	em.mu.Lock()
	defer em.mu.Unlock()

	em.callbacks[eventType] = append(em.callbacks[eventType], callback)
	log.Printf("[LibvirtEvents] Registered callback for event type %d", eventType)
}

// UnregisterAllCallbacks removes all callbacks for a specific event type
func (em *EventManager) UnregisterAllCallbacks(eventType DomainEventType) {
	em.mu.Lock()
	defer em.mu.Unlock()

	delete(em.callbacks, eventType)
	log.Printf("[LibvirtEvents] Unregistered all callbacks for event type %d", eventType)
}

// Start begins listening for domain events
func (em *EventManager) Start() error {
	em.mu.Lock()
	defer em.mu.Unlock()

	if em.running {
		return nil
	}

	// Get a dedicated connection for events
	conn, err := Connect()
	if err != nil {
		return fmt.Errorf("failed to get connection for event manager: %w", err)
	}
	em.conn = conn

	// Register domain lifecycle callback
	callback := libvirt.DomainEventLifecycleCallback(
		func(conn *libvirt.Connect, dom *libvirt.Domain, event *libvirt.DomainEventLifecycle) {
			em.handleDomainEvent(dom, int(event.Event), int(event.Detail))
		},
	)

	callbackID, err := em.conn.DomainEventLifecycleRegister(nil, callback)
	if err != nil {
		Release(em.conn)
		return fmt.Errorf("failed to register domain lifecycle callback: %w", err)
	}

	em.callbackID = callbackID
	em.running = true

	log.Println("[LibvirtEvents] Event manager started")

	return nil
}

// Stop stops the event manager
func (em *EventManager) Stop() error {
	em.mu.Lock()
	defer em.mu.Unlock()

	if !em.running {
		return nil
	}

	em.cancel()

	if em.conn != nil {
		if em.callbackID != 0 {
			if err := em.conn.DomainEventDeregister(em.callbackID); err != nil {
				log.Printf("[LibvirtEvents] Warning: failed to deregister callback: %v", err)
			}
		}
		Release(em.conn)
		em.conn = nil
	}

	em.running = false
	log.Println("[LibvirtEvents] Event manager stopped")

	return nil
}

func (em *EventManager) handleDomainEvent(dom *libvirt.Domain, event int, detail int) {
	// Get domain info
	name, _ := dom.GetName()
	uuid, _ := dom.GetUUIDString()

	details := DomainEventDetails{
		Event:   DomainEventType(event),
		Domain:  name,
		UUID:    uuid,
		Details: getEventDetailString(DomainEventType(event), detail),
	}

	// Try to get current state
	state, _, _ := dom.GetState()
	details.State = DomainState(state)

	log.Printf("[LibvirtEvents] Domain event: %s (%s) - %s", name, uuid, details.Details)

	em.mu.RLock()
	callbacks := em.callbacks[DomainEventType(event)]
	em.mu.RUnlock()

	// Execute callbacks
	for _, callback := range callbacks {
		go func(cb EventCallback) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[LibvirtEvents] Callback panic recovered: %v", r)
				}
			}()
			cb(details)
		}(callback)
	}
}

func getEventDetailString(event DomainEventType, detail int) string {
	switch event {
	case DomainEventDefined:
		switch detail {
		case 0:
			return "Added"
		case 1:
			return "Updated"
		default:
			return "Defined (unknown)"
		}
	case DomainEventUndefined:
		return "Removed"
	case DomainEventStarted:
		switch detail {
		case 0:
			return "Booted"
		case 1:
			return "Migrated"
		case 2:
			return "Restored"
		case 3:
			return "Snapshot"
		case 4:
			return "Wakeup"
		default:
			return "Started (unknown)"
		}
	case DomainEventSuspended:
		switch detail {
		case 0:
			return "Paused"
		case 1:
			return "Migrated"
		case 2:
			return "IOError"
		case 3:
			return "Watchdog"
		case 4:
			return "Restored"
		case 5:
			return "Snapshot"
		case 6:
			return "PMSuspended"
		default:
			return "Suspended (unknown)"
		}
	case DomainEventResumed:
		switch detail {
		case 0:
			return "Unpaused"
		case 1:
			return "Migrated"
		case 2:
			return "Snapshot"
		case 3:
			return "PMSuspended"
		default:
			return "Resumed (unknown)"
		}
	case DomainEventStopped:
		switch detail {
		case 0:
			return "Shutdown"
		case 1:
			return "Destroyed"
		case 2:
			return "Crashed"
		case 3:
			return "Migrated"
		case 4:
			return "Saved"
		case 5:
			return "Failed"
		case 6:
			return "Snapshot"
		default:
			return "Stopped (unknown)"
		}
	case DomainEventShutdown:
		switch detail {
		case 0:
			return "Finished"
		case 1:
			return "On guest request"
		case 2:
			return "On host request"
		default:
			return "Shutdown (unknown)"
		}
	case DomainEventPMSuspended:
		switch detail {
		case 0:
			return "Memory"
		case 1:
			return "Disk"
		default:
			return "PMSuspended (unknown)"
		}
	case DomainEventCrashed:
		switch detail {
		case 0:
			return "Panicked"
		default:
			return "Crashed (unknown)"
		}
	default:
		return "Unknown event"
	}
}

func (s DomainState) String() string {
	switch s {
	case DomainStateNoState:
		return "nostate"
	case DomainStateRunning:
		return "running"
	case DomainStateBlocked:
		return "blocked"
	case DomainStatePaused:
		return "paused"
	case DomainStateShutdown:
		return "shutdown"
	case DomainStateShutoff:
		return "shutoff"
	case DomainStateCrashed:
		return "crashed"
	case DomainStatePMSuspended:
		return "pmsuspended"
	default:
		return "unknown"
	}
}

// Global functions for event management

// RegisterDomainEventCallback registers a callback for domain events
func RegisterDomainEventCallback(eventType DomainEventType, callback EventCallback) error {
	if globalEventManager == nil {
		if err := InitializeEventManager(); err != nil {
			return err
		}
	}
	globalEventManager.RegisterCallback(eventType, callback)
	return nil
}

// StartEventManager starts the global event manager
func StartEventManager() error {
	if globalEventManager == nil {
		if err := InitializeEventManager(); err != nil {
			return err
		}
	}
	return globalEventManager.Start()
}

// StopEventManager stops the global event manager
func StopEventManager() error {
	if globalEventManager == nil {
		return nil
	}
	return globalEventManager.Stop()
}
