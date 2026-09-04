// Package file implements a local Control stream spool.
package file

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

const (
	defaultSegmentBytes       = int64(64 << 20)
	defaultMaxBytes           = int64(1 << 30)
	defaultMaxStreamBytes     = int64(256 << 20)
	defaultMaxRegistrations   = 4096
	defaultMaxReaders         = 8192
	defaultMaxReadersPerPart  = 64
	defaultMaxPartitions      = 16384
	defaultMaxSegments        = 32768
	defaultMaxSegmentsPerPart = 8
	defaultPartitionCharge    = int64(32 << 10)
	defaultSegmentCharge      = int64(8 << 10)
	defaultTerminalTTL        = 24 * time.Hour
	defaultGCInterval         = 5 * time.Minute
	defaultOwnerLockWait      = 20 * time.Millisecond
	ownerLockFilename         = "owner.lock"
)

// Config bounds one file spool. RootDir must be the dedicated versioned spool
// directory, normally <StoreDir>/control/spool/v1.
type Config struct {
	RootDir                   string
	SegmentBytes              int64
	MaxRecordBytes            int
	MaxBytes                  int64
	MaxStreamBytes            int64
	MaxRegistrations          int
	MaxReaders                int
	MaxReadersPerPartition    int
	MaxPartitions             int
	MaxSegments               int
	MaxSegmentsPerPartition   int
	PartitionAllocationCharge int64
	SegmentAllocationCharge   int64
	TerminalTTL               time.Duration
	GCInterval                time.Duration
	OwnerLockWait             time.Duration
	Now                       func() time.Time
}

type segment struct {
	base  streamspool.Offset
	path  string
	bytes int64
}

type partition struct {
	store          *Store
	key            streamspool.Key
	originComplete bool
	dir            string

	mu            sync.Mutex
	notify        chan struct{}
	state         streamspool.State
	low           streamspool.Offset
	high          streamspool.Offset
	segments      []segment
	active        *os.File
	activeBytes   int64
	accounted     int64
	physical      bool
	allocSegments int
	readers       int
	writerActive  bool
	updatedAt     time.Time
}

// Store owns one process epoch and every writer/reader in it.
type Store struct {
	cfg       Config
	root      string
	fsroot    *os.Root
	epoch     streamspool.Epoch
	owner     io.Closer
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	mu            sync.Mutex
	closed        bool
	partitions    map[streamspool.Key]*partition
	current       map[streamspool.LogicalKey]streamspool.Key
	usedBytes     int64
	physicalParts int
	segmentCount  int
	registrations int
	readerCount   int
}

// New opens an exclusively owned spool root. ErrInUse means the application
// must run with authoritative fallbacks instead of creating a second writer.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = withDefaults(cfg)
	root, err := secureRoot(cfg.RootDir)
	if err != nil {
		return nil, err
	}
	fsroot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open stream spool root: %w", err)
	}
	if info, statErr := fsroot.Lstat(ownerLockFilename); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			_ = fsroot.Close()
			return nil, errors.New("stream spool owner lock is not a regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = fsroot.Close()
		return nil, fmt.Errorf("inspect stream spool owner lock: %w", statErr)
	}
	lockCtx, cancel := context.WithTimeout(ctx, cfg.OwnerLockWait)
	owner, err := acquireOwnerLock(lockCtx, fsroot, ownerLockFilename)
	cancel()
	if err != nil {
		_ = fsroot.Close()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: %w", streamspool.ErrInUse, err)
		}
		return nil, fmt.Errorf("stream spool owner lock: %w", err)
	}
	var epoch streamspool.Epoch
	if _, err := rand.Read(epoch[:]); err != nil {
		_ = owner.Close()
		_ = fsroot.Close()
		return nil, fmt.Errorf("stream spool epoch: %w", err)
	}
	// A new process never treats an earlier epoch as exact. Once exclusive
	// ownership is proven, reclaiming those cache-only files is safe and keeps
	// startup accounting simple.
	if err := reclaimOldEpochs(fsroot); err != nil {
		_ = owner.Close()
		_ = fsroot.Close()
		return nil, err
	}
	storeCtx, storeCancel := context.WithCancel(context.Background())
	s := &Store{
		cfg: cfg, root: root, fsroot: fsroot, epoch: epoch, owner: owner,
		ctx: storeCtx, cancel: storeCancel,
		partitions: map[streamspool.Key]*partition{},
		current:    map[streamspool.LogicalKey]streamspool.Key{},
	}
	if cfg.GCInterval > 0 {
		s.wg.Add(1)
		go s.gcLoop()
	}
	return s, nil
}

func withDefaults(cfg Config) Config {
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = defaultSegmentBytes
	}
	if cfg.MaxRecordBytes <= 0 {
		cfg.MaxRecordBytes = streamspool.MaximumRecordBytes
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.MaxStreamBytes <= 0 {
		cfg.MaxStreamBytes = defaultMaxStreamBytes
	}
	if cfg.MaxRegistrations <= 0 {
		cfg.MaxRegistrations = defaultMaxRegistrations
	}
	if cfg.MaxReaders <= 0 {
		cfg.MaxReaders = defaultMaxReaders
	}
	if cfg.MaxReadersPerPartition <= 0 {
		cfg.MaxReadersPerPartition = defaultMaxReadersPerPart
	}
	if cfg.MaxPartitions <= 0 {
		cfg.MaxPartitions = defaultMaxPartitions
	}
	if cfg.MaxSegments <= 0 {
		cfg.MaxSegments = defaultMaxSegments
	}
	if cfg.MaxSegmentsPerPartition <= 0 {
		cfg.MaxSegmentsPerPartition = defaultMaxSegmentsPerPart
	}
	if cfg.PartitionAllocationCharge <= 0 {
		cfg.PartitionAllocationCharge = defaultPartitionCharge
	}
	if cfg.SegmentAllocationCharge <= 0 {
		cfg.SegmentAllocationCharge = defaultSegmentCharge
	}
	if cfg.TerminalTTL <= 0 {
		cfg.TerminalTTL = defaultTerminalTTL
	}
	if cfg.GCInterval < 0 {
		cfg.GCInterval = 0
	} else if cfg.GCInterval == 0 {
		cfg.GCInterval = defaultGCInterval
	}
	if cfg.OwnerLockWait <= 0 {
		cfg.OwnerLockWait = defaultOwnerLockWait
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return cfg
}

// Epoch returns the process-local epoch used in physical keys and cursors.
func (s *Store) Epoch() streamspool.Epoch {
	if s == nil {
		return streamspool.Epoch{}
	}
	return s.epoch
}

func (s *Store) Register(ctx context.Context, logical streamspool.LogicalKey, options streamspool.WriterOptions) (streamspool.Writer, error) {
	if s == nil {
		return nil, streamspool.ErrUnavailable
	}
	if err := validateLogicalKey(logical); err != nil {
		return nil, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, streamspool.ErrClosed
	}
	if key, ok := s.current[logical]; ok {
		if s.partitions[key] != nil {
			return nil, streamspool.ErrInUse
		}
	}
	if s.registrations >= s.cfg.MaxRegistrations || len(s.partitions) >= s.cfg.MaxPartitions {
		return nil, streamspool.ErrLimit
	}
	var incarnation streamspool.Incarnation
	if _, err := rand.Read(incarnation[:]); err != nil {
		return nil, fmt.Errorf("stream spool incarnation: %w", err)
	}
	key := streamspool.Key{LogicalKey: logical, Epoch: s.epoch, Incarnation: incarnation}
	p := &partition{
		store: s, key: key, originComplete: options.OriginComplete,
		dir: partitionRelativeDir(key), notify: make(chan struct{}),
		state: streamspool.StatePending, writerActive: true, updatedAt: s.cfg.Now(),
	}
	s.partitions[key] = p
	s.current[logical] = key
	s.registrations++
	return &writer{partition: p}, nil
}

func (s *Store) Resolve(ctx context.Context, logical streamspool.LogicalKey) (streamspool.Key, streamspool.Bounds, error) {
	if s == nil {
		return streamspool.Key{}, streamspool.Bounds{}, streamspool.ErrUnavailable
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return streamspool.Key{}, streamspool.Bounds{}, err
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return streamspool.Key{}, streamspool.Bounds{}, streamspool.ErrClosed
	}
	key, ok := s.current[logical]
	p := s.partitions[key]
	s.mu.Unlock()
	if !ok || p == nil {
		return streamspool.Key{}, streamspool.Bounds{}, streamspool.ErrNotFound
	}
	bounds, err := p.bounds(ctx)
	return key, bounds, err
}

func (s *Store) Bounds(ctx context.Context, key streamspool.Key) (streamspool.Bounds, error) {
	p, err := s.lookup(ctx, key)
	if err != nil {
		return streamspool.Bounds{}, err
	}
	return p.bounds(ctx)
}

func (s *Store) Reader(ctx context.Context, key streamspool.Key, offset streamspool.Offset) (streamspool.Reader, error) {
	p, err := s.lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.readerForPartition(p, key, offset)
}

// readerForPartition commits a reader lease only while the partition is still
// the exact registered instance found by lookup. removePartition uses the same
// p.mu -> s.mu order, so this membership check closes the lookup/removal window
// without allowing a Reader to escape with a lease on deleted files.
func (s *Store) readerForPartition(p *partition, key streamspool.Key, offset streamspool.Offset) (streamspool.Reader, error) {
	p.mu.Lock()
	if offset < p.low {
		p.mu.Unlock()
		return nil, streamspool.ErrExpired
	}
	if offset > p.high {
		p.mu.Unlock()
		return nil, streamspool.ErrNotFound
	}
	if p.readers >= s.cfg.MaxReadersPerPartition {
		p.mu.Unlock()
		return nil, streamspool.ErrLimit
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		p.mu.Unlock()
		return nil, streamspool.ErrClosed
	}
	if p.state == streamspool.StateStoreClosed || s.partitions[key] != p {
		s.mu.Unlock()
		p.mu.Unlock()
		return nil, streamspool.ErrNotFound
	}
	if s.readerCount >= s.cfg.MaxReaders {
		s.mu.Unlock()
		p.mu.Unlock()
		return nil, streamspool.ErrLimit
	}
	s.readerCount++
	p.readers++
	s.mu.Unlock()
	p.mu.Unlock()
	return &reader{partition: p, key: key, offset: offset, closedCh: make(chan struct{})}, nil
}

func (s *Store) lookup(ctx context.Context, key streamspool.Key) (*partition, error) {
	if s == nil {
		return nil, streamspool.ErrUnavailable
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, streamspool.ErrClosed
	}
	p := s.partitions[key]
	if p == nil {
		return nil, streamspool.ErrNotFound
	}
	return p, nil
}

func (s *Store) Remove(ctx context.Context, key streamspool.Key) error {
	p, err := s.lookup(ctx, key)
	if err != nil {
		return err
	}
	return s.removePartition(p, false)
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
		s.mu.Lock()
		s.closed = true
		parts := make([]*partition, 0, len(s.partitions))
		for _, p := range s.partitions {
			parts = append(parts, p)
		}
		s.mu.Unlock()
		for _, p := range parts {
			p.mu.Lock()
			if p.active != nil {
				s.closeErr = errors.Join(s.closeErr, p.active.Sync(), p.active.Close())
				p.active = nil
			}
			p.state = streamspool.StateStoreClosed
			p.writerActive = false
			p.signalLocked()
			p.mu.Unlock()
		}
		if s.owner != nil {
			s.closeErr = errors.Join(s.closeErr, s.owner.Close())
		}
		if s.fsroot != nil {
			s.closeErr = errors.Join(s.closeErr, s.fsroot.Close())
		}
	})
	return s.closeErr
}

func (s *Store) gcLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.GCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			_ = s.Collect(s.ctx)
		}
	}
}

var _ streamspool.Store = (*Store)(nil)
