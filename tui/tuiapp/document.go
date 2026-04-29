package tuiapp

import (
	"strings"
	"sync/atomic"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// RenderedRow — a single visual line produced by rendering a Block.
// ---------------------------------------------------------------------------

// RenderedRow is one terminal line of output from a block's Render method.
type RenderedRow struct {
	Styled     string // ANSI-colored display text
	Plain      string // plain text for selection/copy
	BlockID    string // originating block ID
	ClickToken string // optional interaction token for row-level hit testing
	PreWrapped bool   // if true, already wrapped to viewport width — skip re-wrapping
}

// StyledRow creates a RenderedRow from a styled line, deriving Plain automatically.
func StyledRow(blockID, styled string) RenderedRow {
	return RenderedRow{Styled: styled, Plain: ansi.Strip(styled), BlockID: blockID}
}

// StyledPlainRow creates a RenderedRow from explicit plain/styled variants.
// Use this when the plain text is the canonical source and the styled text is
// just a presentation transform of that same content.
func StyledPlainRow(blockID, plain, styled string) RenderedRow {
	return RenderedRow{Styled: styled, Plain: plain, BlockID: blockID}
}

func StyledPlainClickableRow(blockID, plain, styled, clickToken string) RenderedRow {
	return RenderedRow{Styled: styled, Plain: plain, BlockID: blockID, ClickToken: clickToken}
}

func StyledPlainClickablePreWrappedRow(blockID, plain, styled, clickToken string) RenderedRow {
	return RenderedRow{Styled: styled, Plain: plain, BlockID: blockID, ClickToken: clickToken, PreWrapped: true}
}

// PlainRow creates a RenderedRow from a plain text line (no ANSI).
func PlainRow(blockID, text string) RenderedRow {
	return RenderedRow{Styled: text, Plain: text, BlockID: blockID}
}

// ---------------------------------------------------------------------------
// BlockKind — identifies the semantic type of a Block.
// ---------------------------------------------------------------------------

type BlockKind string

const (
	BlockTranscript      BlockKind = "transcript"
	BlockAssistant       BlockKind = "assistant"
	BlockReasoning       BlockKind = "reasoning"
	BlockDivider         BlockKind = "divider"
	BlockSubagent        BlockKind = "subagent"
	BlockMainACPTurn     BlockKind = "main_acp_turn"
	BlockParticipantTurn BlockKind = "participant_turn"
	BlockWelcome         BlockKind = "welcome"
)

// ---------------------------------------------------------------------------
// BlockRenderContext — everything a Block needs to render itself.
// ---------------------------------------------------------------------------

type BlockRenderContext struct {
	Width                 int          // viewport content width
	TermWidth             int          // full terminal width
	Theme                 tuikit.Theme // current theme
	ThemeKey              string       // cached theme render key for hot render paths
	SpinnerView           string       // current spinner frame for animated blocks
	ObserveGlamourRender  func()
	ObserveInlineMarkdown func()
}

// ---------------------------------------------------------------------------
// Block — the interface every document block must implement.
// ---------------------------------------------------------------------------

type Block interface {
	BlockID() string
	Kind() BlockKind
	Render(ctx BlockRenderContext) []RenderedRow
}

// ---------------------------------------------------------------------------
// Document — ordered list of blocks, sole source of truth for TUI content.
// ---------------------------------------------------------------------------

var blockIDCounter uint64

func nextBlockID() string {
	n := atomic.AddUint64(&blockIDCounter, 1)
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return "b" + string(buf[i:])
}

type Document struct {
	blocks []Block
	index  map[string]int // blockID → position
}

func NewDocument() *Document {
	return &Document{
		index: make(map[string]int),
	}
}

func (d *Document) Len() int {
	return len(d.blocks)
}

func (d *Document) Append(b Block) {
	d.index[b.BlockID()] = len(d.blocks)
	d.blocks = append(d.blocks, b)
}

// InsertAfter inserts block b immediately after the block with the given
// anchorID. If anchorID is not found, or if anchorID is already followed by
// a block of the same Kind as b, the block is appended at the end.
// Returns the actual insertion index.
func (d *Document) InsertAfter(anchorID string, b Block) int {
	anchorIdx := -1
	if idx, ok := d.index[anchorID]; ok && idx < len(d.blocks) && d.blocks[idx].BlockID() == anchorID {
		anchorIdx = idx
	} else {
		for i, blk := range d.blocks {
			if blk.BlockID() == anchorID {
				anchorIdx = i
				break
			}
		}
	}
	if anchorIdx < 0 {
		d.Append(b)
		return len(d.blocks) - 1
	}
	insertAt := anchorIdx + 1
	// Skip over any existing panels already anchored here (same kind).
	for insertAt < len(d.blocks) && d.blocks[insertAt].Kind() == b.Kind() {
		insertAt++
	}
	// Insert at position.
	d.blocks = append(d.blocks, nil)
	copy(d.blocks[insertAt+1:], d.blocks[insertAt:])
	d.blocks[insertAt] = b
	d.rebuildIndex()
	return insertAt
}

// MoveAfter repositions an existing block immediately after anchorID. If either
// blockID or anchorID cannot be resolved, the document is left unchanged.
// Returns the resulting index and whether a move occurred.
func (d *Document) MoveAfter(blockID string, anchorID string) (int, bool) {
	blockID = strings.TrimSpace(blockID)
	anchorID = strings.TrimSpace(anchorID)
	if d == nil || blockID == "" || anchorID == "" || blockID == anchorID {
		return -1, false
	}
	block := d.Find(blockID)
	if block == nil {
		return -1, false
	}
	if !d.Remove(blockID) {
		return -1, false
	}
	return d.InsertAfter(anchorID, block), true
}

func (d *Document) Find(id string) Block {
	if idx, ok := d.index[id]; ok && idx < len(d.blocks) {
		if d.blocks[idx].BlockID() == id {
			return d.blocks[idx]
		}
	}
	// Fallback linear scan (index may be stale after removals)
	for _, b := range d.blocks {
		if b.BlockID() == id {
			return b
		}
	}
	return nil
}

func (d *Document) Remove(id string) bool {
	for i, b := range d.blocks {
		if b.BlockID() == id {
			d.blocks = append(d.blocks[:i], d.blocks[i+1:]...)
			d.rebuildIndex()
			return true
		}
	}
	return false
}

func (d *Document) Replace(id string, block Block) bool {
	id = strings.TrimSpace(id)
	if d == nil || id == "" || block == nil {
		return false
	}
	for i, existing := range d.blocks {
		if existing.BlockID() != id {
			continue
		}
		d.blocks[i] = block
		d.rebuildIndex()
		return true
	}
	return false
}

func (d *Document) Last() Block {
	if len(d.blocks) == 0 {
		return nil
	}
	return d.blocks[len(d.blocks)-1]
}

func (d *Document) LastOfKind(kind BlockKind) Block {
	for i := len(d.blocks) - 1; i >= 0; i-- {
		if d.blocks[i].Kind() == kind {
			return d.blocks[i]
		}
	}
	return nil
}

func (d *Document) FindByKind(kind BlockKind) []Block {
	var result []Block
	for _, b := range d.blocks {
		if b.Kind() == kind {
			result = append(result, b)
		}
	}
	return result
}

func (d *Document) Clear() {
	d.blocks = d.blocks[:0]
	d.index = make(map[string]int)
}

func (d *Document) Blocks() []Block {
	return d.blocks
}

// RenderAll concatenates rendered output from all blocks.
func (d *Document) RenderAll(ctx BlockRenderContext) []RenderedRow {
	var rows []RenderedRow
	for _, b := range d.blocks {
		rows = append(rows, b.Render(ctx)...)
	}
	return rows
}

func (d *Document) rebuildIndex() {
	d.index = make(map[string]int, len(d.blocks))
	for i, b := range d.blocks {
		d.index[b.BlockID()] = i
	}
}
