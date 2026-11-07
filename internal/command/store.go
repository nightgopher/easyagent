package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a space/goal/result/inbox item does not exist.
var ErrNotFound = errors.New("command: not found")

// state is the on-disk shape of the command center.
type state struct {
	Spaces       []Space     `json:"spaces"`
	Goals        []Goal      `json:"goals"`
	Results      []Result    `json:"results"`
	Inbox        []InboxItem `json:"inbox"`
	NextGoal     int         `json:"next_goal"`
	NextInboxSeq int64       `json:"next_inbox_seq"`
}

// Store is the persistent, thread-safe command center state.
// Everything lives in a single JSON file inside the data dir; the hub is a
// role, its state sits in one place and can be moved between machines.
type Store struct {
	mu        sync.Mutex
	dir       string
	state     state
	inboxHook InboxHook // 新收件箱条目回调（汇报通道），AddInboxItem 内在锁外调用
}

// NewStore opens (or creates) the command center state under dataDir.
func NewStore(dataDir string) *Store {
	s := &Store{dir: dataDir}
	s.load()
	return s
}

func (s *Store) path() string { return filepath.Join(s.dir, "command.json") }

func (s *Store) load() {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return // fresh state
	}
	_ = json.Unmarshal(data, &s.state)
}

// save persists atomically: write temp file, then rename over the target.
func (s *Store) save() error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// ── Spaces ────────────────────────────────────────────────────────────────

// CreateSpace adds a space; repoPath may be empty and set later.
func (s *Store) CreateSpace(name, repoPath string) (Space, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Space{}, errors.New("command: space name is required")
	}
	if repoPath != "" {
		abs, err := filepath.Abs(repoPath)
		if err == nil {
			repoPath = abs
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range s.state.Spaces {
		if sp.Name == name {
			return Space{}, fmt.Errorf("command: space %q already exists", name)
		}
	}
	sp := Space{
		ID:        newID("sp"),
		Name:      name,
		RepoPath:  repoPath,
		CreatedAt: time.Now(),
	}
	s.state.Spaces = append(s.state.Spaces, sp)
	return sp, s.save()
}

// Spaces lists spaces ordered by name.
func (s *Store) Spaces() []Space {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Space(nil), s.state.Spaces...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Space returns one space by ID.
func (s *Store) Space(id string) (Space, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range s.state.Spaces {
		if sp.ID == id {
			return sp, nil
		}
	}
	return Space{}, ErrNotFound
}

// SetSpaceRepo points a space at a local git repository.
func (s *Store) SetSpaceRepo(id, repoPath string) error {
	if repoPath != "" {
		abs, err := filepath.Abs(repoPath)
		if err == nil {
			repoPath = abs
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Spaces {
		if s.state.Spaces[i].ID == id {
			s.state.Spaces[i].RepoPath = repoPath
			return s.save()
		}
	}
	return ErrNotFound
}

// SetSpaceNotes updates the space notes (visible memory for goal kickoffs).
func (s *Store) SetSpaceNotes(id, notes string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Spaces {
		if s.state.Spaces[i].ID == id {
			s.state.Spaces[i].Notes = notes
			return s.save()
		}
	}
	return ErrNotFound
}

// ── Goals ─────────────────────────────────────────────────────────────────

// CreateGoal adds a goal to a space and numbers it.
func (s *Store) CreateGoal(spaceID, title, description string, done []string) (Goal, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Goal{}, errors.New("command: goal title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var spaceOK bool
	for _, sp := range s.state.Spaces {
		if sp.ID == spaceID {
			spaceOK = true
			break
		}
	}
	if !spaceOK {
		return Goal{}, fmt.Errorf("command: space %q not found", spaceID)
	}
	if s.state.NextGoal == 0 {
		s.state.NextGoal = 1
	}
	items := make([]DoneItem, 0, len(done))
	for _, d := range done {
		if t := strings.TrimSpace(d); t != "" {
			items = append(items, DoneItem{Text: t})
		}
	}
	now := time.Now()
	g := Goal{
		Number:      s.state.NextGoal,
		SpaceID:     spaceID,
		Title:       title,
		Description: description,
		Done:        items,
		Status:      GoalOpen,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.state.NextGoal++
	s.state.Goals = append(s.state.Goals, g)
	return g, s.save()
}

// Goals lists goals; when spaceID is empty, all goals are returned.
// Open goals first, newest first within a status.
func (s *Store) Goals(spaceID string) []Goal {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Goal
	for _, g := range s.state.Goals {
		if spaceID == "" || g.SpaceID == spaceID {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == GoalOpen) != (out[j].Status == GoalOpen) {
			return out[i].Status == GoalOpen
		}
		return out[i].Number > out[j].Number
	})
	return out
}

// Goal returns one goal by number.
func (s *Store) Goal(number int) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.state.Goals {
		if g.Number == number {
			return g, nil
		}
	}
	return Goal{}, ErrNotFound
}

// SetDoneItem checks or unchecks one definition-of-done line.
func (s *Store) SetDoneItem(number, index int, check bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Goals {
		if s.state.Goals[i].Number != number {
			continue
		}
		if index < 0 || index >= len(s.state.Goals[i].Done) {
			return fmt.Errorf("command: done item %d out of range", index)
		}
		s.state.Goals[i].Done[index].Check = check
		s.state.Goals[i].UpdatedAt = time.Now()
		return s.save()
	}
	return ErrNotFound
}

// SetGoalStatus moves a goal between open and closed.
func (s *Store) SetGoalStatus(number int, status string) error {
	if status != GoalOpen && status != GoalClosed {
		return fmt.Errorf("command: unknown goal status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Goals {
		if s.state.Goals[i].Number == number {
			s.state.Goals[i].Status = status
			s.state.Goals[i].UpdatedAt = time.Now()
			return s.save()
		}
	}
	return ErrNotFound
}

// ── Results ───────────────────────────────────────────────────────────────

// AddResult records a result if its ID is new; returns whether it was added.
func (s *Store) AddResult(res Result) (bool, error) {
	if res.ID == "" || res.SpaceID == "" {
		return false, errors.New("command: result id and space are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.state.Results {
		if r.ID == res.ID {
			return false, nil
		}
	}
	if res.DiscoveredAt.IsZero() {
		res.DiscoveredAt = time.Now()
	}
	s.state.Results = append(s.state.Results, res)
	return true, s.save()
}

// Results lists results, newest first. goalNumber > 0 returns that goal's
// results; goalNumber == 0 returns only unassigned results; goalNumber < 0
// returns everything. spaceID "" means all spaces.
func (s *Store) Results(spaceID string, goalNumber int) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Result
	for _, r := range s.state.Results {
		if spaceID != "" && r.SpaceID != spaceID {
			continue
		}
		if goalNumber > 0 && r.GoalNumber != goalNumber {
			continue
		}
		if goalNumber == 0 && r.GoalNumber != 0 {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DiscoveredAt.After(out[j].DiscoveredAt) })
	return out
}

// AssignResult attaches an unassigned result to a goal.
func (s *Store) AssignResult(resultID string, goalNumber int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	goalOK := false
	for _, g := range s.state.Goals {
		if g.Number == goalNumber {
			goalOK = true
			break
		}
	}
	if !goalOK {
		return fmt.Errorf("command: goal #%d not found", goalNumber)
	}
	for i := range s.state.Results {
		if s.state.Results[i].ID == resultID {
			s.state.Results[i].GoalNumber = goalNumber
			return s.save()
		}
	}
	return ErrNotFound
}

// AddManualResult registers a deliverable by hand (file path, URL, …).
func (s *Store) AddManualResult(spaceID string, goalNumber int, ref, title string) (Result, error) {
	res := Result{
		ID:         newID("res"),
		SpaceID:    spaceID,
		GoalNumber: goalNumber,
		Kind:       ResultManual,
		Ref:        ref,
		Title:      title,
	}
	added, err := s.AddResult(res)
	if err != nil {
		return Result{}, err
	}
	if !added {
		return Result{}, errors.New("command: duplicate result")
	}
	return res, nil
}

// ── Inbox ────────────────────────────────────────────────────────────────

// AddInboxItem appends one item; IDs are generated locally.
func (s *Store) AddInboxItem(it InboxItem) (InboxItem, error) {
	if it.Title == "" {
		return InboxItem{}, errors.New("command: inbox title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.NextInboxSeq++
	it.ID = fmt.Sprintf("in-%d-%d", time.Now().Unix(), s.state.NextInboxSeq)
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now()
	}
	s.state.Inbox = append(s.state.Inbox, it)
	if err := s.save(); err != nil {
		return it, err
	}
	if s.inboxHook != nil {
		go func(h InboxHook, item InboxItem) {
			defer func() { _ = recover() }() // 通知通道故障不拖垮状态机
			h(item)
		}(s.inboxHook, it)
	}
	return it, nil
}

// Inbox lists items, newest first, pending (not done) by default.
func (s *Store) Inbox(includeDone bool) []InboxItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []InboxItem
	for _, it := range s.state.Inbox {
		if !includeDone && it.Done {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// AckInbox marks an item handled.
func (s *Store) AckInbox(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Inbox {
		if s.state.Inbox[i].ID == id {
			s.state.Inbox[i].Done = true
			return s.save()
		}
	}
	return ErrNotFound
}

// AssignInboxResult points an inbox item's unassigned result at a goal
// (creating the goal from the item title when goalNumber is 0).
func (s *Store) AssignInboxResult(itemID string, goalNumber int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var item *InboxItem
	for i := range s.state.Inbox {
		if s.state.Inbox[i].ID == itemID {
			item = &s.state.Inbox[i]
			break
		}
	}
	if item == nil {
		return 0, ErrNotFound
	}
	if goalNumber == 0 { // 新建目标，标题沿用收件箱条目（去掉「未归属的结果：」前缀）
		if s.state.NextGoal == 0 {
			s.state.NextGoal = 1
		}
		now := time.Now()
		title := strings.TrimPrefix(item.Title, "未归属的结果：")
		s.state.Goals = append(s.state.Goals, Goal{
			Number:    s.state.NextGoal,
			SpaceID:   item.SpaceID,
			Title:     title,
			Status:    GoalOpen,
			CreatedAt: now,
			UpdatedAt: now,
		})
		goalNumber = s.state.NextGoal
		s.state.NextGoal++
	}
	goalOK := false
	for _, g := range s.state.Goals {
		if g.Number == goalNumber {
			goalOK = true
			break
		}
	}
	if !goalOK {
		return 0, fmt.Errorf("command: goal #%d not found", goalNumber)
	}
	for i := range s.state.Results {
		if s.state.Results[i].ID == item.ResultID {
			s.state.Results[i].GoalNumber = goalNumber
		}
	}
	item.Done = true
	return goalNumber, s.save()
}

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d-%04d", prefix, time.Now().UnixNano(), time.Now().Nanosecond()%10000)
}
