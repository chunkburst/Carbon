// Package templates persists reusable explicit task blueprints. Instantiation delegates to
// store.CreateExplicit so templates cannot bypass type/importance/project semantics.
package templates

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

	"carbon/internal/store"
	"carbon/internal/task"

	"gopkg.in/yaml.v3"
)

const dataDir = "templates"

var (
	ErrNotFound        = errors.New("template not found")
	ErrInvalidTemplate = errors.New("invalid template")
)

type Template struct {
	ID          string       `yaml:"id" json:"id"`
	Name        string       `yaml:"name" json:"name"`
	Title       string       `yaml:"title" json:"title"`
	Body        string       `yaml:"body,omitempty" json:"body,omitempty"`
	ProjectID   string       `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	ClusterWide bool         `yaml:"cluster_wide,omitempty" json:"cluster_wide,omitempty"`
	Type        string       `yaml:"type" json:"type"`
	Importance  string       `yaml:"importance" json:"importance"`
	Priority    string       `yaml:"priority,omitempty" json:"priority,omitempty"`
	Labels      []string     `yaml:"labels,omitempty" json:"labels,omitempty"`
	Deps        []string     `yaml:"deps,omitempty" json:"deps,omitempty"`
	Checks      []task.Check `yaml:"checks,omitempty" json:"checks,omitempty"`
	Parent      string       `yaml:"parent,omitempty" json:"parent,omitempty"`
	CreatedAt   string       `yaml:"created_at" json:"created_at"`
	CreatedBy   string       `yaml:"created_by,omitempty" json:"created_by,omitempty"`
	UpdatedAt   string       `yaml:"updated_at" json:"updated_at"`
	UpdatedBy   string       `yaml:"updated_by,omitempty" json:"updated_by,omitempty"`
	Version     string       `yaml:"-" json:"version,omitempty"`
}

func (t Template) ETag() string {
	if t.Version == "" {
		return ""
	}
	return `"` + t.Version + `"`
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

func (m *Manager) Create(ctx context.Context, actor string, template Template) (Template, error) {
	if m.Store == nil {
		return Template{}, errors.New("templates manager has no store")
	}
	if template.ID == "" {
		id, err := mintID()
		if err != nil {
			return Template{}, err
		}
		template.ID = id
	}
	if err := m.validateTemplate(template); err != nil {
		return Template{}, err
	}
	now := m.now().Format(time.RFC3339)
	template.CreatedAt, template.CreatedBy, template.UpdatedAt, template.UpdatedBy = now, actor, now, actor
	var out Template
	err := m.Store.Write(ctx, actor, "create template", func(tx *store.WriteTx) error {
		if _, err := tx.ReadData(dataDir, filename(template.ID)); err == nil {
			return fmt.Errorf("%w: duplicate id", ErrInvalidTemplate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := yaml.Marshal(template)
		if err != nil {
			return err
		}
		if err := tx.WriteData(dataDir, filename(template.ID), data); err != nil {
			return err
		}
		template.Version = fingerprint(data)
		out = clone(template)
		return nil
	})
	return out, err
}

func (m *Manager) Save(ctx context.Context, actor string, template Template, expectedVersion string) (Template, error) {
	if m.Store == nil {
		return Template{}, errors.New("templates manager has no store")
	}
	if err := m.validateTemplate(template); err != nil {
		return Template{}, err
	}
	var out Template
	err := m.Store.Write(ctx, actor, "save template", func(tx *store.WriteTx) error {
		current, raw, err := readTx(tx, template.ID)
		if err != nil {
			return err
		}
		if err := matchVersion(fingerprint(raw), expectedVersion); err != nil {
			return err
		}
		template.CreatedAt, template.CreatedBy = current.CreatedAt, current.CreatedBy
		template.UpdatedAt, template.UpdatedBy = m.now().Format(time.RFC3339), actor
		data, err := yaml.Marshal(template)
		if err != nil {
			return err
		}
		if err := tx.WriteData(dataDir, filename(template.ID), data); err != nil {
			return err
		}
		template.Version = fingerprint(data)
		out = clone(template)
		return nil
	})
	return out, err
}

func (m *Manager) Get(id string) (Template, error) {
	if m.Store == nil {
		return Template{}, errors.New("templates manager has no store")
	}
	if err := validateID(id); err != nil {
		return Template{}, err
	}
	data, err := m.Store.ReadData(dataDir, filename(id))
	if errors.Is(err, os.ErrNotExist) {
		return Template{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Template{}, err
	}
	var template Template
	if err := yaml.Unmarshal(data, &template); err != nil {
		return Template{}, err
	}
	if err := m.validateTemplate(template); err != nil || template.ID != id {
		return Template{}, fmt.Errorf("%w: malformed %s", ErrInvalidTemplate, id)
	}
	template.Version = fingerprint(data)
	return template, nil
}

func (m *Manager) List() ([]Template, error) {
	if m.Store == nil {
		return nil, errors.New("templates manager has no store")
	}
	names, err := m.Store.ListData(dataDir)
	if err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		template, err := m.Get(strings.TrimSuffix(name, ".yaml"))
		if err != nil {
			return nil, err
		}
		out = append(out, template)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *Manager) Delete(ctx context.Context, actor, id, expectedVersion string) error {
	if m.Store == nil {
		return errors.New("templates manager has no store")
	}
	if err := validateID(id); err != nil {
		return err
	}
	return m.Store.Write(ctx, actor, "delete template", func(tx *store.WriteTx) error {
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

// InstantiateInput permits narrowly scoped overrides. Pointer fields distinguish an
// override to empty (especially project_id cluster-wide) from "use template value".
type InstantiateInput struct {
	TemplateID      string
	Actor           string
	ExpectedVersion string
	Title           *string
	Body            *string
	ProjectID       *string
	Type            *string
	Importance      *string
	Priority        *string
	Labels          *[]string
	Deps            *[]string
	Parent          *string
	Checks          *[]task.Check
}

func (m *Manager) Instantiate(ctx context.Context, input InstantiateInput) (*store.Doc, error) {
	if m.Store == nil {
		return nil, errors.New("templates manager has no store")
	}
	if err := validateID(input.TemplateID); err != nil {
		return nil, err
	}

	// Read/version validation and task creation deliberately share one Store.Write
	// transaction. A concurrent Save/Delete therefore cannot land between a client
	// observing a template ETag and the resulting task being persisted.
	var created *store.Doc
	err := m.Store.Write(ctx, input.Actor, "instantiate template", func(tx *store.WriteTx) error {
		template, raw, err := readTx(tx, input.TemplateID)
		if err != nil {
			return err
		}
		if err := m.validateTemplate(template); err != nil {
			return err
		}
		if err := matchVersion(fingerprint(raw), input.ExpectedVersion); err != nil {
			return err
		}
		draft := templateDraft(template, input)
		created, err = tx.CreateExplicit(input.Actor, draft, m.now())
		return err
	})
	return created, err
}

func templateDraft(template Template, input InstantiateInput) store.ExplicitDraft {
	draft := store.ExplicitDraft{
		Title: template.Title, Body: template.Body, ProjectID: template.ProjectID, ClusterWide: template.ClusterWide,
		Type: template.Type, Importance: template.Importance, Priority: template.Priority,
		Labels: slices.Clone(template.Labels), Deps: slices.Clone(template.Deps), Parent: template.Parent,
		Checks: slices.Clone(template.Checks),
	}
	if input.Title != nil {
		draft.Title = *input.Title
	}
	if input.Body != nil {
		draft.Body = *input.Body
	}
	if input.ProjectID != nil {
		draft.ProjectID, draft.ClusterWide = *input.ProjectID, *input.ProjectID == ""
	}
	if input.Type != nil {
		draft.Type = *input.Type
	}
	if input.Importance != nil {
		draft.Importance = *input.Importance
	}
	if input.Priority != nil {
		draft.Priority = *input.Priority
	}
	if input.Labels != nil {
		draft.Labels = slices.Clone(*input.Labels)
	}
	if input.Deps != nil {
		draft.Deps = slices.Clone(*input.Deps)
	}
	if input.Parent != nil {
		draft.Parent = *input.Parent
	}
	if input.Checks != nil {
		draft.Checks = slices.Clone(*input.Checks)
	}
	return draft
}

func (m *Manager) validateTemplate(t Template) error {
	if err := validateID(t.ID); err != nil {
		return err
	}
	if strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("%w: name and title are required", ErrInvalidTemplate)
	}
	if !task.ValidImportanceKey(t.Importance) {
		return fmt.Errorf("%w: importance", ErrInvalidTemplate)
	}
	if !task.ValidPriority(t.Priority) {
		return fmt.Errorf("%w: priority", ErrInvalidTemplate)
	}
	if t.Type == "" {
		return fmt.Errorf("%w: type", ErrInvalidTemplate)
	}
	if m.Store != nil {
		cfg, err := m.Store.Config()
		if err != nil {
			return err
		}
		if !cfg.TypeCatalog().Allowed(t.Type) {
			return fmt.Errorf("%w: type %q", ErrInvalidTemplate, t.Type)
		}
	}
	return nil
}

func readTx(tx *store.WriteTx, id string) (Template, []byte, error) {
	data, err := tx.ReadData(dataDir, filename(id))
	if errors.Is(err, os.ErrNotExist) {
		return Template{}, nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Template{}, nil, err
	}
	var template Template
	if err := yaml.Unmarshal(data, &template); err != nil {
		return Template{}, nil, err
	}
	if err := validateID(template.ID); err != nil || template.ID != id {
		return Template{}, nil, fmt.Errorf("%w: malformed %s", ErrInvalidTemplate, id)
	}
	template.Version = fingerprint(data)
	return template, data, nil
}

func filename(id string) string { return id + ".yaml" }

func validateID(id string) error {
	if !strings.HasPrefix(id, "tpl_") || len(id) != len("tpl_")+20 {
		return fmt.Errorf("%w: id %q", ErrInvalidTemplate, id)
	}
	for _, r := range id[len("tpl_"):] {
		if !((r >= 'a' && r <= 'f') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("%w: id %q", ErrInvalidTemplate, id)
		}
	}
	return nil
}

func mintID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "tpl_" + hex.EncodeToString(b[:]), nil
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func matchVersion(current, expected string) error {
	if expected == "" {
		return nil
	}
	expected = strings.Trim(strings.TrimSpace(expected), "\"")
	if current != expected {
		return fmt.Errorf("%w: expected %q, got %q", store.ErrVersionMismatch, expected, current)
	}
	return nil
}

func clone(t Template) Template {
	t.Labels, t.Deps, t.Checks = slices.Clone(t.Labels), slices.Clone(t.Deps), slices.Clone(t.Checks)
	return t
}
