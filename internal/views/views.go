// Package views persists named search filters under .carbon/views. Views are data, not a
// UI concern: both MCP and HTTP can list/apply the same saved definition.
package views

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"carbon/internal/search"
	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

const dataDir = "views"

var (
	ErrNotFound    = errors.New("saved view not found")
	ErrInvalidView = errors.New("invalid saved view")
)

type View struct {
	ID        string       `yaml:"id" json:"id"`
	Name      string       `yaml:"name" json:"name"`
	Query     search.Query `yaml:"query" json:"query"`
	CreatedAt string       `yaml:"created_at" json:"created_at"`
	CreatedBy string       `yaml:"created_by,omitempty" json:"created_by,omitempty"`
	UpdatedAt string       `yaml:"updated_at" json:"updated_at"`
	UpdatedBy string       `yaml:"updated_by,omitempty" json:"updated_by,omitempty"`
	Version   string       `yaml:"-" json:"version,omitempty"`
}

func (v View) ETag() string {
	if v.Version == "" {
		return ""
	}
	return `"` + v.Version + `"`
}

type Manager struct {
	Store *store.Store
	Now   func() time.Time
}

func New(s *store.Store, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{Store: s, Now: now}
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

// Create assigns a stable id if absent and persists a new view. A supplied id is useful
// for import/export, but must have the safe `view_` namespace to avoid colliding files.
func (m *Manager) Create(ctx context.Context, actor string, view View) (View, error) {
	if m.Store == nil {
		return View{}, errors.New("views manager has no store")
	}
	if view.ID == "" {
		id, err := mintID()
		if err != nil {
			return View{}, err
		}
		view.ID = id
	}
	if err := validate(view); err != nil {
		return View{}, err
	}
	now := m.now().Format(time.RFC3339)
	view.CreatedAt, view.CreatedBy, view.UpdatedAt, view.UpdatedBy = now, actor, now, actor
	var out View
	err := m.Store.Write(ctx, actor, "create saved view", func(tx *store.WriteTx) error {
		name := filename(view.ID)
		if _, err := tx.ReadData(dataDir, name); err == nil {
			return fmt.Errorf("%w: %s", ErrInvalidView, view.ID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := yaml.Marshal(view)
		if err != nil {
			return err
		}
		if err := tx.WriteData(dataDir, name, data); err != nil {
			return err
		}
		view.Version = fingerprint(data)
		out = clone(view)
		return nil
	})
	return out, err
}

// Save updates an existing view with an optional raw Version/ETag precondition.
func (m *Manager) Save(ctx context.Context, actor string, view View, expectedVersion string) (View, error) {
	if m.Store == nil {
		return View{}, errors.New("views manager has no store")
	}
	if err := validate(view); err != nil {
		return View{}, err
	}
	var out View
	err := m.Store.Write(ctx, actor, "save saved view", func(tx *store.WriteTx) error {
		current, raw, err := readTx(tx, view.ID)
		if err != nil {
			return err
		}
		if err := matchVersion(fingerprint(raw), expectedVersion); err != nil {
			return err
		}
		view.CreatedAt, view.CreatedBy = current.CreatedAt, current.CreatedBy
		view.UpdatedAt, view.UpdatedBy = m.now().Format(time.RFC3339), actor
		data, err := yaml.Marshal(view)
		if err != nil {
			return err
		}
		if err := tx.WriteData(dataDir, filename(view.ID), data); err != nil {
			return err
		}
		view.Version = fingerprint(data)
		out = clone(view)
		return nil
	})
	return out, err
}

func (m *Manager) Get(id string) (View, error) {
	if m.Store == nil {
		return View{}, errors.New("views manager has no store")
	}
	if err := validateID(id); err != nil {
		return View{}, err
	}
	data, err := m.Store.ReadData(dataDir, filename(id))
	if errors.Is(err, os.ErrNotExist) {
		return View{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return View{}, err
	}
	var view View
	if err := yaml.Unmarshal(data, &view); err != nil {
		return View{}, err
	}
	if err := validate(view); err != nil || view.ID != id {
		return View{}, fmt.Errorf("%w: malformed %s", ErrInvalidView, id)
	}
	view.Version = fingerprint(data)
	return view, nil
}

func (m *Manager) List() ([]View, error) {
	if m.Store == nil {
		return nil, errors.New("views manager has no store")
	}
	names, err := m.Store.ListData(dataDir)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		id := strings.TrimSuffix(name, ".yaml")
		view, err := m.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	slices.SortFunc(out, func(a, b View) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *Manager) Delete(ctx context.Context, actor, id, expectedVersion string) error {
	if m.Store == nil {
		return errors.New("views manager has no store")
	}
	if err := validateID(id); err != nil {
		return err
	}
	return m.Store.Write(ctx, actor, "delete saved view", func(tx *store.WriteTx) error {
		_, raw, err := readTx(tx, id)
		if err != nil {
			return err
		}
		if err := matchVersion(fingerprint(raw), expectedVersion); err != nil {
			return err
		}
		return tx.DeleteData(dataDir, filename(id))
	})
}

// Apply filters/ranks an already-loaded collection using the persisted query.
func Apply(view View, docs []*store.Doc) []search.Result { return search.Search(docs, view.Query) }

func readTx(tx *store.WriteTx, id string) (View, []byte, error) {
	data, err := tx.ReadData(dataDir, filename(id))
	if errors.Is(err, os.ErrNotExist) {
		return View{}, nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return View{}, nil, err
	}
	var view View
	if err := yaml.Unmarshal(data, &view); err != nil {
		return View{}, nil, err
	}
	if err := validate(view); err != nil || view.ID != id {
		return View{}, nil, fmt.Errorf("%w: malformed %s", ErrInvalidView, id)
	}
	view.Version = fingerprint(data)
	return view, data, nil
}

func filename(id string) string { return id + ".yaml" }

func validate(v View) error {
	if err := validateID(v.ID); err != nil {
		return err
	}
	if strings.TrimSpace(v.Name) == "" || len([]rune(v.Name)) > 120 {
		return fmt.Errorf("%w: name", ErrInvalidView)
	}
	return nil
}

func validateID(id string) error {
	if !strings.HasPrefix(id, "view_") || len(id) != len("view_")+20 {
		return fmt.Errorf("%w: id %q", ErrInvalidView, id)
	}
	for _, r := range id[len("view_"):] {
		if !((r >= 'a' && r <= 'f') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("%w: id %q", ErrInvalidView, id)
		}
	}
	return nil
}

func mintID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "view_" + hex.EncodeToString(b[:]), nil
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func matchVersion(current, expected string) error {
	if expected == "" {
		return nil
	}
	expected = strings.TrimSpace(expected)
	if len(expected) >= 2 && expected[0] == '"' && expected[len(expected)-1] == '"' {
		expected = expected[1 : len(expected)-1]
	}
	if current != expected {
		return fmt.Errorf("%w: expected %q, got %q", store.ErrVersionMismatch, expected, current)
	}
	return nil
}

func clone(view View) View {
	view.Query.Labels = slices.Clone(view.Query.Labels)
	if view.Query.ProjectID != nil {
		value := *view.Query.ProjectID
		view.Query.ProjectID = &value
	}
	return view
}
