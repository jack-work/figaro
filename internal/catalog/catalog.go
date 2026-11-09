package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/models"
)

type Mapping struct {
	List        string           `toml:"list,omitempty"`
	ID          []string         `toml:"id,omitempty"`
	Name        []string         `toml:"name,omitempty"`
	Context     []string         `toml:"context,omitempty"`
	Long        []string         `toml:"long,omitempty"`
	MaxOutput   []string         `toml:"max_output,omitempty"`
	MaxPrompt   []string         `toml:"max_prompt,omitempty"`
	Images      []string         `toml:"images,omitempty"`
	ImageBytes  []string         `toml:"max_prompt_image_size,omitempty"`
	ImageCount  []string         `toml:"max_prompt_images,omitempty"`
	Tools       []string         `toml:"tools,omitempty"`
	Reasoning   []string         `toml:"reasoning,omitempty"`
	Visible     []string         `toml:"visible,omitempty"`
	Endpoints   []string         `toml:"endpoints,omitempty"`
	More        string           `toml:"more,omitempty"`
	Cursor      string           `toml:"cursor,omitempty"`
	CursorParam string           `toml:"cursor_param,omitempty"`
	Facts       map[string]Facts `toml:"facts,omitempty"`
}

type Facts struct {
	Context    int   `toml:"context,omitempty"`
	Long       int   `toml:"long,omitempty"`
	MaxOutput  int   `toml:"max_output,omitempty"`
	Images     *bool `toml:"images,omitempty"`
	ImageBytes int   `toml:"max_prompt_image_size,omitempty"`
	ImageCount int   `toml:"max_prompt_images,omitempty"`
	Tools      *bool `toml:"tools,omitempty"`
	Reasoning  *bool `toml:"reasoning,omitempty"`
}

type Override struct {
	TextOnly   *bool `toml:"text_only,omitempty"`
	Context    int   `toml:"context,omitempty"`
	Long       int   `toml:"long,omitempty"`
	MaxOutput  int   `toml:"max_output,omitempty"`
	ImageBytes int   `toml:"max_prompt_image_size,omitempty"`
	ImageCount int   `toml:"max_prompt_images,omitempty"`
}

func (m Mapping) With(over *Mapping) Mapping {
	if over == nil {
		return m
	}
	pick := func(base, o []string) []string {
		if o != nil {
			return o
		}
		return base
	}
	word := func(base, o string) string {
		if o != "" {
			return o
		}
		return base
	}
	out := Mapping{
		List:        word(m.List, over.List),
		ID:          pick(m.ID, over.ID),
		Name:        pick(m.Name, over.Name),
		Context:     pick(m.Context, over.Context),
		Long:        pick(m.Long, over.Long),
		MaxOutput:   pick(m.MaxOutput, over.MaxOutput),
		MaxPrompt:   pick(m.MaxPrompt, over.MaxPrompt),
		Images:      pick(m.Images, over.Images),
		ImageBytes:  pick(m.ImageBytes, over.ImageBytes),
		ImageCount:  pick(m.ImageCount, over.ImageCount),
		Tools:       pick(m.Tools, over.Tools),
		Reasoning:   pick(m.Reasoning, over.Reasoning),
		Visible:     pick(m.Visible, over.Visible),
		Endpoints:   pick(m.Endpoints, over.Endpoints),
		More:        word(m.More, over.More),
		Cursor:      word(m.Cursor, over.Cursor),
		CursorParam: word(m.CursorParam, over.CursorParam),
	}
	if len(m.Facts)+len(over.Facts) > 0 {
		out.Facts = make(map[string]Facts, len(m.Facts)+len(over.Facts))
		for k, f := range m.Facts {
			out.Facts[k] = f
		}
		for k, f := range over.Facts {
			out.Facts[k] = f
		}
	}
	return out
}

type File struct {
	Catalog *Mapping            `toml:"catalog,omitempty"`
	Models  map[string]Override `toml:"models,omitempty"`
}

func Decode(data []byte) (File, error) {
	var f File
	if _, err := toml.Decode(string(data), &f); err != nil {
		return File{}, err
	}
	return f, nil
}

type Model struct {
	ID string
	models.Entry
}

type Reader struct {
	list      []string
	id        []path
	name      []path
	context   []path
	long      []path
	maxOutput []path
	maxPrompt []path
	images    []path
	imgBytes  []path
	imgCount  []path
	tools     []path
	reasoning []path
	visible   []path
	endpoints []path
	more      []string
	cursor    []string
	param     string
	facts     []fact
	overrides map[string]Override
}

type form int

const (
	formValue form = iota
	formHas
	formNonempty
)

type path struct {
	keys []string
	form form
	arg  string
}

type fact struct {
	prefix string
	open   bool
	Facts
}

func New(m Mapping, overrides map[string]Override) (*Reader, error) {
	r := &Reader{list: split(m.List), more: split(m.More), cursor: split(m.Cursor), param: m.CursorParam, overrides: overrides}
	if (m.More == "") != (m.Cursor == "") || (m.Cursor != "") != (m.CursorParam != "") {
		return nil, fmt.Errorf("catalog: more, cursor and cursor_param go together")
	}
	for _, f := range []struct {
		key  string
		in   []string
		into *[]path
	}{
		{"id", m.ID, &r.id}, {"name", m.Name, &r.name}, {"context", m.Context, &r.context},
		{"long", m.Long, &r.long}, {"max_output", m.MaxOutput, &r.maxOutput}, {"max_prompt", m.MaxPrompt, &r.maxPrompt}, {"images", m.Images, &r.images},
		{"max_prompt_image_size", m.ImageBytes, &r.imgBytes}, {"max_prompt_images", m.ImageCount, &r.imgCount},
		{"tools", m.Tools, &r.tools}, {"reasoning", m.Reasoning, &r.reasoning}, {"visible", m.Visible, &r.visible},
		{"endpoints", m.Endpoints, &r.endpoints},
	} {
		for _, s := range f.in {
			p, err := parsePath(s)
			if err != nil {
				return nil, fmt.Errorf("catalog: %s: %w", f.key, err)
			}
			*f.into = append(*f.into, p)
		}
	}
	if len(r.id) == 0 {
		r.id = []path{{keys: []string{"id"}}}
	}
	for glob, f := range m.Facts {
		n := normalize(glob)
		open := strings.HasSuffix(n, "*")
		n = strings.TrimSuffix(n, "*")
		if n == "" || strings.ContainsAny(n, "*?[") {
			return nil, fmt.Errorf("catalog: facts %q: a pattern is an id, or an id prefix ending in *", glob)
		}
		r.facts = append(r.facts, fact{prefix: n, open: open, Facts: f})
	}
	sort.Slice(r.facts, func(i, j int) bool {
		a, b := r.facts[i], r.facts[j]
		if len(a.prefix) != len(b.prefix) {
			return len(a.prefix) > len(b.prefix)
		}
		return !a.open && b.open
	})
	return r, nil
}

func parsePath(s string) (path, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return path{}, fmt.Errorf("an empty path")
	}
	p := path{keys: split(fields[0])}
	switch {
	case len(fields) == 1:
	case len(fields) == 2 && fields[1] == "nonempty":
		p.form = formNonempty
	case len(fields) == 2 && strings.HasPrefix(fields[1], "has:") && len(fields[1]) > len("has:"):
		p.form, p.arg = formHas, strings.TrimPrefix(fields[1], "has:")
	default:
		return path{}, fmt.Errorf("%q: a path is a dotted key, then nothing, has:<value> or nonempty", s)
	}
	return p, nil
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

func (r *Reader) Paged() bool { return r.param != "" }

func (r *Reader) NextURL(base, cursor string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(r.param, cursor)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (r *Reader) Page(body io.Reader) (listed []Model, cursor string, err error) {
	dec := json.NewDecoder(body)
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, "", fmt.Errorf("catalog: %w", err)
	}
	list, ok := at(root, r.list).([]any)
	if !ok {
		where := strings.Join(r.list, ".")
		if where == "" {
			where = "the top level"
		}
		return nil, "", fmt.Errorf("catalog: no list at %s", where)
	}
	listed = make([]Model, 0, len(list))
	for _, item := range list {
		id, ok := firstString(item, r.id)
		if !ok {
			continue
		}
		listed = append(listed, Model{ID: id, Entry: r.entry(id, item)})
	}
	if more, _ := at(root, r.more).(bool); more && r.Paged() {
		cursor, _ = at(root, r.cursor).(string)
	}
	return listed, cursor, nil
}

func (r *Reader) Seed(id string) (models.Entry, bool) {
	e := r.entry(id, nil)
	return e, len(e.Src) > 0
}

func (r *Reader) entry(id string, item any) models.Entry {
	var e models.Entry
	if v, ok := firstString(item, r.name); ok {
		e.Name = v
		e.Mark("name", models.SourceAPI)
	}
	if v, ok := firstInt(item, r.context); ok {
		e.Context.Default = v
		e.Context.Long, _ = firstInt(item, r.long)
		e.Mark("context", models.SourceAPI)
	}
	setInt(&e, "max_output", &e.MaxOutput, item, r.maxOutput)
	setInt(&e, "max_prompt", &e.MaxPrompt, item, r.maxPrompt)
	setInt(&e, "max_prompt_image_size", &e.ImageBytes, item, r.imgBytes)
	setInt(&e, "max_prompt_images", &e.ImageCount, item, r.imgCount)
	setBool(&e, "images", &e.Images, item, r.images)
	setBool(&e, "tools", &e.Tools, item, r.tools)
	setBool(&e, "reasoning", &e.Reasoning, item, r.reasoning)
	if v, ok := firstBool(item, r.visible); ok {
		e.Hidden = !v
		e.Mark("hidden", models.SourceAPI)
	}
	if v, ok := firstStrings(item, r.endpoints); ok {
		e.Endpoints = v
		e.Mark("endpoints", models.SourceAPI)
	}
	if f, ok := r.factFor(id); ok {
		applyFacts(&e, f)
	}
	if o, ok := r.overrides[id]; ok {
		applyOverride(&e, o)
	}
	return e
}

func setInt(e *models.Entry, field string, into *int, item any, paths []path) {
	if v, ok := firstInt(item, paths); ok {
		*into = v
		e.Mark(field, models.SourceAPI)
	}
}

func setBool(e *models.Entry, field string, into **bool, item any, paths []path) {
	if v, ok := firstBool(item, paths); ok {
		*into = &v
		e.Mark(field, models.SourceAPI)
	}
}

func (r *Reader) factFor(id string) (Facts, bool) {
	n := normalize(id)
	for _, f := range r.facts {
		if n == f.prefix || (f.open && strings.HasPrefix(n, f.prefix+"-")) {
			return f.Facts, true
		}
	}
	return Facts{}, false
}

func applyFacts(e *models.Entry, f Facts) {
	if e.Source("context") == "" && (f.Context > 0 || f.Long > 0) {
		e.Context = models.Context{Default: f.Context, Long: f.Long}
		e.Mark("context", models.SourceMapping)
	}
	fill := func(field string, into *int, v int) {
		if v > 0 && e.Source(field) == "" {
			*into = v
			e.Mark(field, models.SourceMapping)
		}
	}
	fill("max_output", &e.MaxOutput, f.MaxOutput)
	fill("max_prompt_image_size", &e.ImageBytes, f.ImageBytes)
	fill("max_prompt_images", &e.ImageCount, f.ImageCount)
	flag := func(field string, into **bool, v *bool) {
		if v != nil && e.Source(field) == "" {
			b := *v
			*into = &b
			e.Mark(field, models.SourceMapping)
		}
	}
	flag("images", &e.Images, f.Images)
	flag("tools", &e.Tools, f.Tools)
	flag("reasoning", &e.Reasoning, f.Reasoning)
}

func applyOverride(e *models.Entry, o Override) {
	if o.Context > 0 || o.Long > 0 {
		c := models.Context{Default: o.Context, Long: o.Long}
		if c.Default == 0 {
			c.Default = e.Context.Default
		}
		if o.Long == 0 {
			c.Long = c.Default
		}
		e.Context = c
		e.Mark("context", models.SourceConfig)
	}
	if o.MaxOutput > 0 {
		e.MaxOutput = o.MaxOutput
		e.Mark("max_output", models.SourceConfig)
	}
	if o.ImageBytes > 0 {
		e.ImageBytes = o.ImageBytes
		e.Mark("max_prompt_image_size", models.SourceConfig)
	}
	if o.ImageCount > 0 {
		e.ImageCount = o.ImageCount
		e.Mark("max_prompt_images", models.SourceConfig)
	}
	if o.TextOnly != nil {
		takes := !*o.TextOnly
		e.Images = &takes
		e.Mark("images", models.SourceConfig)
	}
}

func normalize(id string) string {
	m := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.ReplaceAll(m, ".", "-")
}

func at(v any, keys []string) any {
	for _, k := range keys {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = obj[k]
	}
	return v
}

func value(item any, p path) any {
	v := at(item, p.keys)
	switch p.form {
	case formHas:
		list, ok := v.([]any)
		if !ok {
			return nil
		}
		for _, x := range list {
			if s, ok := x.(string); ok && s == p.arg {
				return true
			}
		}
		return false
	case formNonempty:
		switch x := v.(type) {
		case []any:
			return len(x) > 0
		case map[string]any:
			return len(x) > 0
		}
		return nil
	}
	return v
}

func firstString(item any, paths []path) (string, bool) {
	for _, p := range paths {
		if s, ok := value(item, p).(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func firstInt(item any, paths []path) (int, bool) {
	for _, p := range paths {
		n, ok := value(item, p).(json.Number)
		if !ok {
			continue
		}
		if i, err := n.Int64(); err == nil && i > 0 {
			return int(i), true
		}
	}
	return 0, false
}

func firstBool(item any, paths []path) (bool, bool) {
	for _, p := range paths {
		if b, ok := value(item, p).(bool); ok {
			return b, true
		}
	}
	return false, false
}

func firstStrings(item any, paths []path) ([]string, bool) {
	for _, p := range paths {
		list, ok := value(item, p).([]any)
		if !ok {
			continue
		}
		out := make([]string, 0, len(list))
		for _, x := range list {
			s, ok := x.(string)
			if !ok {
				out = nil
				break
			}
			out = append(out, s)
		}
		if out != nil {
			return out, true
		}
	}
	return nil, false
}
