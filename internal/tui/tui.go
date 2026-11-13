// Package tui is Canopy's terminal front end: the schema tree, node details,
// live reads and the matching gNMIc command, for terminals where a browser
// isn't at hand (for example over SSH on a jump host). It uses the same
// service as the CLI and the web UI.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/format"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/schema"
)

// Options for Run.
type Options struct {
	Session   *app.Session // nil: browse the schema only
	Models    string       // what was loaded, for the status line
	Shell     string       // bash or powershell
	Qualified bool
	Screen    tcell.Screen // nil: the terminal (tests pass a simulation screen)
}

type ui struct {
	ctx    context.Context
	svc    *app.Service
	opt    Options
	tree   *schema.Tree
	app    *tview.Application
	view   *tview.TreeView
	right  *tview.Pages
	detail *tview.TextView
	data   *tview.TextView
	caps   *tview.TextView
	cmd    *tview.TextView
	status *tview.TextView
	pages  *tview.Pages
	search *tview.InputField
	hits   *tview.List
	marked map[string]bool
	nodes  map[int]*tview.TreeNode
	yaml   bool
	last   *live.Result
}

const keysHelp = "[::b]/[::-] search  [::b]space[::-] mark  [::b]g[::-] get node  [::b]G[::-] get marked  [::b]d[::-] details  [::b]c[::-] capabilities  [::b]y[::-] YAML/JSON  [::b]p[::-] qualified  [::b]x[::-] clear marks  [::b]q[::-] quit"

// Run shows the terminal UI until the user quits.
func Run(ctx context.Context, svc *app.Service, opt Options) error {
	t, err := svc.Tree()
	if err != nil {
		return err
	}
	if opt.Shell == "" {
		opt.Shell = "bash"
	}
	u := &ui{ctx: ctx, svc: svc, opt: opt, tree: t, app: tview.NewApplication(), marked: map[string]bool{}, nodes: map[int]*tview.TreeNode{}, yaml: true}
	u.build()
	if opt.Screen != nil {
		u.app.SetScreen(opt.Screen)
	}
	return u.app.Run()
}

func (u *ui) build() {
	root := tview.NewTreeNode("schema").SetReference(-1)
	u.view = tview.NewTreeView().SetRoot(root).SetCurrentNode(root).SetTopLevel(1)
	u.view.SetBorder(true).SetTitle(" Schema ")
	u.addChildren(root, -1)
	if kids := root.GetChildren(); len(kids) > 0 {
		u.view.SetCurrentNode(kids[0])
	}
	u.view.SetSelectedFunc(func(n *tview.TreeNode) { u.toggle(n) })
	u.view.SetChangedFunc(func(n *tview.TreeNode) { u.showDetail(n) })

	text := func(title string) *tview.TextView {
		v := tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWrap(true)
		v.SetBorder(true).SetTitle(title)
		return v
	}
	u.detail, u.data, u.caps = text(" Details "), text(" Data "), text(" Capabilities ")
	u.right = tview.NewPages().AddPage("details", u.detail, true, true).AddPage("data", u.data, true, false).AddPage("caps", u.caps, true, false)
	u.cmd = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	u.cmd.SetBorder(true).SetTitle(" gNMIc ")
	u.status = tview.NewTextView().SetDynamicColors(true)
	help := tview.NewTextView().SetDynamicColors(true).SetText(keysHelp)

	body := tview.NewFlex().AddItem(u.view, 0, 2, true).AddItem(u.right, 0, 3, false)
	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.status, 1, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(u.cmd, 4, 0, false).
		AddItem(help, 1, 0, false)

	u.search = tview.NewInputField().SetLabel("Search: ").SetFieldWidth(0)
	u.hits = tview.NewList().ShowSecondaryText(true).SetHighlightFullLine(true)
	u.search.SetChangedFunc(func(q string) { u.runSearch(q) })
	u.search.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEscape:
			u.closeSearch()
		case tcell.KeyEnter, tcell.KeyDown, tcell.KeyTab:
			if u.hits.GetItemCount() > 0 {
				u.app.SetFocus(u.hits)
			}
		}
	})
	u.hits.SetDoneFunc(func() { u.closeSearch() })
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(u.search, 1, 0, true).AddItem(u.hits, 0, 1, false)
	box.SetBorder(true).SetTitle(" Find a node (Enter: go there, Esc: close) ")
	modal := tview.NewFlex().AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).AddItem(nil, 2, 0, false).AddItem(box, 0, 1, true).AddItem(nil, 2, 0, false), 0, 3, true).
		AddItem(nil, 0, 1, false)

	u.pages = tview.NewPages().AddPage("main", main, true, true).AddPage("search", modal, true, false)
	u.app.SetRoot(u.pages, true).EnableMouse(true)
	u.app.SetInputCapture(u.keys)

	u.renderCaps()
	u.updateStatus()
	u.updateCmd()
	u.showDetail(u.view.GetCurrentNode())
}

func (u *ui) keys(ev *tcell.EventKey) *tcell.EventKey {
	if front, _ := u.pages.GetFrontPage(); front == "search" {
		return ev
	}
	if ev.Key() == tcell.KeyCtrlC {
		u.app.Stop()
		return nil
	}
	if ev.Key() != tcell.KeyRune {
		return ev
	}
	n := u.view.GetCurrentNode()
	switch ev.Rune() {
	case 'q':
		u.app.Stop()
	case '/':
		u.search.SetText("")
		u.hits.Clear()
		u.pages.ShowPage("search")
		u.app.SetFocus(u.search)
	case ' ':
		if id := ref(n); id >= 0 {
			p := u.pathOf(u.tree.Node(id))
			u.marked[p] = !u.marked[p]
			if !u.marked[p] {
				delete(u.marked, p)
			}
			u.label(n, id)
			u.updateCmd()
			u.updateStatus()
		}
	case 'x':
		u.marked = map[string]bool{}
		for id, tn := range u.nodes {
			u.label(tn, id)
		}
		u.updateCmd()
		u.updateStatus()
	case 'g':
		if id := ref(n); id >= 0 {
			u.get([]string{u.pathOf(u.tree.Node(id))})
		}
	case 'G':
		if ps := u.markedPaths(); len(ps) > 0 {
			u.get(ps)
		} else {
			u.flash("Mark paths with space first")
		}
	case 'd':
		u.right.SwitchToPage("details")
	case 'c':
		u.right.SwitchToPage("caps")
	case 'y':
		u.yaml = !u.yaml
		u.renderData()
	case 'p':
		u.opt.Qualified = !u.opt.Qualified
		// marks follow the new path form
		old := u.marked
		u.marked = map[string]bool{}
		for id, tn := range u.nodes {
			if n := u.tree.Node(id); n != nil && (old[qualified(n)] || old[n.Path]) {
				u.marked[u.pathOf(n)] = true
			}
			u.label(tn, id)
		}
		u.updateCmd()
		u.updateStatus()
		u.showDetail(u.view.GetCurrentNode())
	default:
		return ev
	}
	return nil
}

func ref(n *tview.TreeNode) int {
	if n == nil {
		return -1
	}
	if id, ok := n.GetReference().(int); ok {
		return id
	}
	return -1
}

// qualified is the module-qualified path, with the openconfig origin where
// the top module is an OpenConfig one (as in the web UI).
func qualified(n *schema.Node) string {
	p := n.QPath
	if strings.HasPrefix(p, "/openconfig-") {
		return "openconfig:" + p
	}
	return p
}

func (u *ui) pathOf(n *schema.Node) string {
	if u.opt.Qualified {
		return qualified(n)
	}
	return n.Path
}

func (u *ui) markedPaths() []string {
	var out []string
	for p := range u.marked {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (u *ui) addChildren(tn *tview.TreeNode, id int) {
	tn.ClearChildren()
	for _, c := range u.tree.Children(id) {
		cn := tview.NewTreeNode("").SetReference(c.ID).SetSelectable(true)
		u.nodes[c.ID] = cn
		u.label(cn, c.ID)
		if c.HasChildren {
			cn.AddChild(tview.NewTreeNode("…").SetSelectable(false)) // placeholder until expanded
			cn.SetExpanded(false)
		}
		tn.AddChild(cn)
	}
}

var kindMark = map[string]string{"container": "▸", "list": "≡", "leaf": "•", "leaf-list": "⋮"}

func (u *ui) label(tn *tview.TreeNode, id int) {
	n := u.tree.Node(id)
	if n == nil {
		return
	}
	name := tview.Escape(n.Name)
	if n.Parent < 0 || u.tree.Node(n.Parent).Module != n.Module {
		name = "[gray]" + tview.Escape(n.Module) + ":[-]" + name
	}
	if n.Kind == "list" && len(n.Keys) > 0 {
		name += "[gray][" + tview.Escape(strings.Join(n.Keys, ",")) + "][-]"
	}
	mark := "  "
	if u.marked[u.pathOf(n)] {
		mark = "[green]● [-]"
	}
	color := "white"
	if !n.Config {
		color = "silver"
	}
	tn.SetText(mark + kindMark[n.Kind] + " [" + color + "]" + name + "[-]")
}

func (u *ui) toggle(tn *tview.TreeNode) {
	id := ref(tn)
	if id < 0 {
		return
	}
	n := u.tree.Node(id)
	if !n.HasChildren {
		u.get([]string{u.pathOf(n)})
		return
	}
	if !tn.IsExpanded() {
		u.addChildren(tn, id)
	}
	tn.SetExpanded(!tn.IsExpanded())
}

func (u *ui) showDetail(tn *tview.TreeNode) {
	id := ref(tn)
	n := u.tree.Node(id)
	if n == nil {
		u.detail.SetText("")
		return
	}
	var b strings.Builder
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "[yellow]%-12s[-] %s\n", k, tview.Escape(v))
		}
	}
	fmt.Fprintf(&b, "[::b]%s[::-]  [gray]%s[-]\n\n", tview.Escape(n.Name), n.Kind)
	row("path", u.pathOf(n))
	if u.opt.Qualified {
		row("plain", n.Path)
	}
	row("module", n.Module)
	if n.Config {
		row("access", "config (read-write)")
	} else {
		row("access", "state (read-only)")
	}
	if len(n.Keys) > 0 {
		row("keys", strings.Join(n.Keys, ", "))
	}
	if t := n.Type; t != nil {
		ty := t.Name
		if t.Base != "" && t.Base != t.Name {
			ty += " (" + t.Base + ")"
		}
		row("type", ty)
		row("range", t.Range)
		row("length", t.Length)
		if len(t.Enums) > 0 {
			row("values", strings.Join(t.Enums, ", "))
		}
	}
	row("units", n.Units)
	row("default", strings.Join(n.Default, ", "))
	row("choice", n.Choice)
	if n.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", tview.Escape(n.Description))
	}
	u.detail.SetText(b.String()).ScrollToBeginning()
	if len(u.marked) == 0 {
		u.updateCmd()
	}
}

func (u *ui) get(paths []string) {
	if u.opt.Session == nil {
		u.flash("Not connected: start with a target (-a or --conn) to read data")
		return
	}
	u.right.SwitchToPage("data")
	u.data.SetText("[gray]Reading " + tview.Escape(strings.Join(paths, " ")) + " …[-]")
	go func() {
		ctx, cancel := context.WithTimeout(u.ctx, 60*time.Second)
		defer cancel()
		res, err := u.svc.Get(ctx, u.opt.Session.ID, live.GetRequest{Paths: paths})
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				e := apperr.As(err)
				u.data.SetText("[red]" + tview.Escape(e.Error()) + "[-]")
				return
			}
			u.last = res
			u.renderData()
		})
	}()
}

func (u *ui) renderData() {
	r := u.last
	if r == nil {
		return
	}
	var text string
	var err error
	if u.yaml {
		text, err = format.YAML(r.Data)
	} else {
		text, err = format.JSON(r.Data)
	}
	if err != nil {
		text = err.Error()
	}
	if r.Empty {
		text = "(no data)"
	}
	u.data.SetTitle(fmt.Sprintf(" Data · %d leaves · %d ms ", r.LeafCount, r.ElapsedMs))
	u.data.SetText(tview.Escape(text)).ScrollToBeginning()
}

func (u *ui) renderCaps() {
	s := u.opt.Session
	if s == nil {
		u.caps.SetText("[gray]Not connected.[-]")
		return
	}
	var b strings.Builder
	c := s.Capabilities
	fmt.Fprintf(&b, "[yellow]gNMI[-] %s\n[yellow]encodings[-] %s\n\n", c.GNMIVersion, strings.Join(c.Encodings, ", "))
	for _, m := range c.Models {
		fmt.Fprintf(&b, "%s  [gray]%s %s[-]\n", tview.Escape(m.Name), tview.Escape(m.Version), tview.Escape(m.Organization))
	}
	u.caps.SetTitle(fmt.Sprintf(" Capabilities · %d models ", len(c.Models)))
	u.caps.SetText(b.String())
}

func (u *ui) updateCmd() {
	paths := u.markedPaths()
	if len(paths) == 0 {
		if n := u.tree.Node(ref(u.view.GetCurrentNode())); n != nil {
			paths = []string{u.pathOf(n)}
		}
	}
	if len(paths) == 0 || u.opt.Session == nil {
		u.cmd.SetText("[gray]Connect to a device to see the command.[-]")
		return
	}
	out, err := u.svc.Generate(u.opt.Session.ID, nil, generate.Request{Paths: paths, Mode: "get", Shell: u.opt.Shell})
	if err != nil {
		u.cmd.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
		return
	}
	c := out.Commands[0]
	text := tview.Escape(c.Text)
	if c.Note != "" {
		text += "\n[gray]" + tview.Escape(c.Note) + "[-]"
	}
	u.cmd.SetText(text)
}

func (u *ui) updateStatus() {
	dev := "[gray]not connected[-]"
	if s := u.opt.Session; s != nil {
		dev = "[green]" + tview.Escape(s.Label) + "[-]"
		if s.Address != s.Label {
			dev += " " + tview.Escape(s.Address)
		}
	}
	q := "plain"
	if u.opt.Qualified {
		q = "qualified"
	}
	sum := u.tree.Summary()
	u.status.SetText(fmt.Sprintf(" [::b]Canopy[::-]  %s  ·  %d marked  ·  %s paths  ·  %s, %d modules",
		dev, len(u.marked), q, tview.Escape(u.opt.Models), len(sum.Modules)))
}

func (u *ui) flash(msg string) {
	u.status.SetText(" [yellow]" + tview.Escape(msg) + "[-]")
	go func() {
		time.Sleep(3 * time.Second)
		u.app.QueueUpdateDraw(u.updateStatus)
	}()
}

func (u *ui) runSearch(q string) {
	u.hits.Clear()
	for _, r := range u.tree.Search(q, 200) {
		id := r.ID
		u.hits.AddItem(tview.Escape(r.Name)+"  [gray]"+r.Kind+"[-]", tview.Escape(u.pathOf(r.Node)), 0, func() {
			u.closeSearch()
			u.reveal(id)
		})
	}
}

func (u *ui) closeSearch() {
	u.pages.HidePage("search")
	u.app.SetFocus(u.view)
}

// reveal expands the ancestors of id and selects it.
func (u *ui) reveal(id int) {
	for _, a := range append(u.tree.Ancestors(id), id) {
		tn := u.nodes[a]
		if tn == nil {
			continue
		}
		if a != id && !tn.IsExpanded() {
			u.addChildren(tn, a)
			tn.SetExpanded(true)
		}
	}
	if tn := u.nodes[id]; tn != nil {
		u.view.SetCurrentNode(tn)
		u.showDetail(tn)
	}
}
