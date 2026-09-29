package tui

import (
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// Vim mode for the composer (editor_mode "vim"). The engine works on the
// prompt as runes and a cursor offset; vimKey moves it in and out of the
// textarea. Insert mode is the textarea as usual; normal mode takes the
// printable keys, and keys like enter and ctrl+o keep their actions.

// vimBuf is the prompt: its text and the cursor's rune offset.
type vimBuf struct {
	r []rune
	c int
}

type vimSnap struct {
	text string
	c    int
}

type vimEditor struct {
	insert   bool
	pending  []rune // keys of a normal-mode command still being typed
	reg      string // the unnamed register
	regLines bool   // reg holds whole lines
	undo     []vimSnap
	// insStart is the prompt before the command that entered insert
	// mode, so undo takes back the command and the typing together.
	insStart *vimSnap
	// For "." : the last change command, and the text typed after it
	// when it entered insert mode.
	lastChange []rune
	lastInsert []rune
	recording  bool
	typed      []rune
	find       []rune // the last f, F, t or T and its character, for ; and ,
}

const vimUndoLimit = 100

func newVim() *vimEditor { return &vimEditor{insert: true} }

// reset starts a fresh prompt in insert mode; the register and "." stay.
func (v *vimEditor) reset() {
	v.insert, v.pending, v.undo, v.insStart, v.recording, v.typed = true, nil, nil, nil, false, nil
}

// vimCmd is a parsed normal-mode command: [count] [operator [count]] key.
type vimCmd struct {
	count int    // 0 when none was typed
	op    rune   // d, c, y, or 0
	key   []rune // the command, motion or text object ("x", "w", "fx", "iw", "d" in dd)
}

// parseVim parses the keys typed so far. done is false while more keys
// are needed; ok is false when they can't make a command.
func parseVim(s []rune) (cmd vimCmd, done, ok bool) {
	i := 0
	readCount := func() int {
		n := 0
		for i < len(s) && (s[i] >= '1' && s[i] <= '9' || n > 0 && s[i] == '0') {
			n = n*10 + int(s[i]-'0')
			i++
		}
		return n
	}
	c1 := readCount()
	if i == len(s) {
		return cmd, false, true
	}
	if strings.ContainsRune("dcy", s[i]) {
		cmd.op = s[i]
		i++
		c2 := readCount()
		cmd.count = c1
		if c2 > 0 {
			cmd.count = max(c1, 1) * c2
		}
		if i == len(s) {
			return cmd, false, true
		}
		switch {
		case s[i] == cmd.op:
			cmd.key = s[i : i+1]
			return cmd, true, i+1 == len(s)
		case s[i] == 'i' || s[i] == 'a':
			if i+1 == len(s) {
				return cmd, false, true
			}
			cmd.key = s[i : i+2]
			return cmd, true, i+2 == len(s) && strings.ContainsRune(`wW"'`+"`"+`()b[]{}B<>`, s[i+1])
		}
		n, complete, valid := motionLen(s[i:])
		cmd.key = s[i:min(i+n, len(s))]
		return cmd, complete, valid
	}
	cmd.count = c1
	switch {
	case strings.ContainsRune("xXDCsSpPJu~iIaAoO.", s[i]):
		cmd.key = s[i : i+1]
		return cmd, true, i+1 == len(s)
	case s[i] == 'r':
		if i+1 == len(s) {
			return cmd, false, true
		}
		cmd.key = s[i : i+2]
		return cmd, true, i+2 == len(s)
	}
	n, complete, valid := motionLen(s[i:])
	cmd.key = s[i:min(i+n, len(s))]
	return cmd, complete, valid
}

// motionLen is how many keys the motion at the start of s takes.
func motionLen(s []rune) (n int, complete, valid bool) {
	switch {
	case strings.ContainsRune("hjklwWeEbB0^$G;, ", s[0]):
		return 1, true, len(s) == 1
	case s[0] == 'g':
		if len(s) == 1 {
			return 2, false, true
		}
		return 2, true, s[1] == 'g' && len(s) == 2
	case strings.ContainsRune("fFtT", s[0]):
		if len(s) == 1 {
			return 2, false, true
		}
		return 2, true, len(s) == 2
	}
	return 1, true, false
}

// key feeds one normal-mode key. hist is -1 or 1 when k or j can't move
// and the prompt history should be recalled instead.
func (v *vimEditor) key(b *vimBuf, k rune) (hist int) {
	v.pending = append(v.pending, k)
	cmd, done, ok := parseVim(v.pending)
	if !ok {
		v.pending = nil
		return 0
	}
	if !done {
		return 0
	}
	keys := v.pending
	v.pending = nil
	before := vimSnap{string(b.r), b.c}
	if cmd.key[0] == '.' {
		v.repeat(b, cmd.count)
	} else {
		hist = v.run(b, cmd)
		if isChange(cmd) {
			v.lastChange = keys
			if v.insert {
				v.recording, v.typed = true, nil
			}
		}
	}
	switch {
	case v.insert:
		v.insStart = &before
	case cmd.key[0] == 'u' && cmd.op == 0:
	case string(b.r) != before.text:
		v.pushUndo(before)
	}
	if !v.insert {
		b.c = b.normalCol(b.c)
	}
	return hist
}

func isChange(cmd vimCmd) bool {
	if cmd.op == 'd' || cmd.op == 'c' {
		return true
	}
	return cmd.op == 0 && strings.ContainsRune("xXDCsSpPJr~iIaAoO", cmd.key[0])
}

func (v *vimEditor) pushUndo(s vimSnap) {
	v.undo = append(v.undo, s)
	if len(v.undo) > vimUndoLimit {
		v.undo = v.undo[1:]
	}
}

// escape leaves insert mode, stepping back onto the last character typed.
func (v *vimEditor) escape(b *vimBuf) {
	v.insert = false
	if v.recording {
		v.lastInsert, v.recording = v.typed, false
	}
	if v.insStart != nil && v.insStart.text != string(b.r) {
		v.pushUndo(*v.insStart)
	}
	v.insStart = nil
	if b.c > b.lineStart(b.c) {
		b.c--
	}
	b.c = b.normalCol(b.c)
}

// record notes a key typed in insert mode, for ".".
func (v *vimEditor) record(k, text string, newline bool) {
	if !v.recording {
		return
	}
	switch {
	case newline:
		v.typed = append(v.typed, '\n')
	case k == "backspace":
		if len(v.typed) > 0 {
			v.typed = v.typed[:len(v.typed)-1]
		}
	case text != "":
		v.typed = append(v.typed, []rune(text)...)
	}
}

// repeat runs the last change again, with its typed text.
func (v *vimEditor) repeat(b *vimBuf, count int) {
	if len(v.lastChange) == 0 {
		return
	}
	cmd, _, _ := parseVim(v.lastChange)
	if count > 0 {
		cmd.count = count
	}
	v.run(b, cmd)
	if v.insert {
		b.insert(b.c, v.lastInsert)
		b.c += len(v.lastInsert)
		v.insert = false
		if b.c > b.lineStart(b.c) {
			b.c--
		}
	}
}

func (v *vimEditor) run(b *vimBuf, cmd vimCmd) (hist int) {
	n := max(cmd.count, 1)
	k := cmd.key[0]
	if cmd.op != 0 {
		switch {
		case k == cmd.op: // dd, cc, yy
			last := b.c
			for range n - 1 {
				if next, ok := b.lineDown(last); ok {
					last = next
				}
			}
			v.apply(b, cmd.op, b.c, last, true)
		case (k == 'i' || k == 'a') && len(cmd.key) == 2:
			if s, e, ok := b.object(b.c, cmd.key[1], k == 'a'); ok {
				v.apply(b, cmd.op, s, e, false)
			}
		default:
			mo := v.motion(b, cmd.key, cmd.count, cmd.op)
			if !mo.ok {
				return 0
			}
			end := mo.to
			if mo.inclusive && !mo.linewise {
				end = min(end+1, len(b.r))
			}
			if mo.linewise {
				v.apply(b, cmd.op, b.c, mo.to, true)
			} else if end < b.c {
				v.apply(b, cmd.op, end, b.c, false)
			} else {
				v.apply(b, cmd.op, b.c, end, false)
			}
		}
		return 0
	}
	switch k {
	case 'x':
		v.run(b, vimCmd{count: cmd.count, op: 'd', key: []rune("l")})
	case 'X':
		v.run(b, vimCmd{count: cmd.count, op: 'd', key: []rune("h")})
	case 'D':
		v.run(b, vimCmd{op: 'd', key: []rune("$")})
	case 'C':
		v.run(b, vimCmd{op: 'c', key: []rune("$")})
	case 's':
		v.run(b, vimCmd{count: cmd.count, op: 'c', key: []rune("l")})
	case 'S':
		v.run(b, vimCmd{count: cmd.count, op: 'c', key: []rune("c")})
	case 'i':
		v.insert = true
	case 'a':
		v.insert = true
		if b.c < b.lineEnd(b.c) {
			b.c++
		}
	case 'I':
		v.insert = true
		b.c = b.firstNonBlank(b.c)
	case 'A':
		v.insert = true
		b.c = b.lineEnd(b.c)
	case 'o':
		v.insert = true
		b.c = b.lineEnd(b.c)
		b.insert(b.c, []rune("\n"))
		b.c++
	case 'O':
		v.insert = true
		b.c = b.lineStart(b.c)
		b.insert(b.c, []rune("\n"))
	case 'p', 'P':
		v.put(b, k == 'p', n)
	case 'J':
		for range max(n-1, 1) {
			b.join()
		}
	case 'r':
		if b.c+n <= b.lineEnd(b.c) {
			for i := range n {
				b.r[b.c+i] = cmd.key[1]
			}
			b.c += n - 1
		}
	case '~':
		for i := 0; i < n && b.c < b.lineEnd(b.c); i++ {
			ch := b.r[b.c]
			if unicode.IsUpper(ch) {
				b.r[b.c] = unicode.ToLower(ch)
			} else {
				b.r[b.c] = unicode.ToUpper(ch)
			}
			b.c++
		}
	case 'u':
		for range n {
			v.undoOnce(b)
		}
	default:
		mo := v.motion(b, cmd.key, cmd.count, 0)
		if mo.ok {
			b.c = mo.to
			if mo.linewise && (k == 'G' || k == 'g') {
				b.c = b.firstNonBlank(b.c)
			}
		} else if cmd.count == 0 && (k == 'j' || k == 'k') {
			if k == 'j' {
				return 1
			}
			return -1
		}
	}
	return 0
}

// undoOnce restores the latest snapshot that differs from the prompt.
func (v *vimEditor) undoOnce(b *vimBuf) {
	for len(v.undo) > 0 {
		s := v.undo[len(v.undo)-1]
		v.undo = v.undo[:len(v.undo)-1]
		if s.text != string(b.r) {
			b.r, b.c = []rune(s.text), min(s.c, len([]rune(s.text)))
			return
		}
	}
}

// apply runs operator op over [a, z) or, linewise, over the lines from
// a's to z's.
func (v *vimEditor) apply(b *vimBuf, op rune, a, z int, linewise bool) {
	if linewise {
		a, z = min(a, z), max(a, z)
		s, e := b.lineStart(a), b.lineEnd(z)
		v.reg, v.regLines = string(b.r[s:e]), true
		switch op {
		case 'd':
			switch {
			case e < len(b.r):
				b.delete(s, e+1)
			case s > 0:
				b.delete(s-1, e)
				s = b.lineStart(s - 1)
			default:
				b.delete(s, e)
			}
			b.c = b.firstNonBlank(min(s, len(b.r)))
		case 'c':
			b.delete(s, e)
			b.c, v.insert = s, true
		case 'y':
			b.c = min(b.c, a)
		}
		return
	}
	a, z = max(a, 0), min(z, len(b.r))
	if a >= z {
		if op == 'c' {
			b.c, v.insert = a, true
		}
		return
	}
	v.reg, v.regLines = string(b.r[a:z]), false
	switch op {
	case 'd':
		b.delete(a, z)
		b.c = a
	case 'c':
		b.delete(a, z)
		b.c, v.insert = a, true
	case 'y':
		b.c = a
	}
}

// put pastes the register n times, after the cursor (p) or before it (P).
func (v *vimEditor) put(b *vimBuf, after bool, n int) {
	if v.reg == "" && !v.regLines {
		return
	}
	text := []rune(strings.Repeat(v.reg, n))
	if v.regLines {
		lines := []rune(strings.Repeat(v.reg+"\n", n))
		switch {
		case !after:
			at := b.lineStart(b.c)
			b.insert(at, lines)
			b.c = b.firstNonBlank(at)
		case b.lineEnd(b.c) == len(b.r):
			at := len(b.r)
			b.insert(at, append([]rune("\n"), lines[:len(lines)-1]...))
			b.c = b.firstNonBlank(at + 1)
		default:
			at := b.lineEnd(b.c) + 1
			b.insert(at, lines)
			b.c = b.firstNonBlank(at)
		}
		return
	}
	at := b.c
	if after && b.c < b.lineEnd(b.c) {
		at++
	}
	b.insert(at, text)
	b.c = at + len(text) - 1
}

type vimMotion struct {
	to                  int
	linewise, inclusive bool
	ok                  bool
}

// motion finds where key moves the cursor. op is the operator it serves,
// or 0 for a plain move.
func (v *vimEditor) motion(b *vimBuf, key []rune, count int, op rune) vimMotion {
	n := max(count, 1)
	c := b.c
	big := key[0] == 'W' || key[0] == 'E' || key[0] == 'B'
	switch key[0] {
	case 'h':
		return vimMotion{to: max(b.lineStart(c), c-n), ok: true}
	case 'l', ' ':
		to := min(c+n, b.lineEnd(c))
		if op == 0 {
			to = b.normalCol(to)
		}
		return vimMotion{to: to, ok: true}
	case 'j', 'k':
		to := c
		for range n {
			next, ok := b.lineDown(to)
			if key[0] == 'k' {
				next, ok = b.lineUp(to)
			}
			if !ok {
				return vimMotion{}
			}
			to = next
		}
		return vimMotion{to: to, linewise: true, ok: true}
	case 'w', 'W':
		if op == 'c' && c < len(b.r) && !unicode.IsSpace(b.r[c]) {
			// cw changes to the end of the word, like ce.
			to := c
			for i := range n {
				if i == 0 && b.atWordEnd(to, big) {
					continue
				}
				to = b.wordEnd(to, big)
			}
			return vimMotion{to: to, inclusive: true, ok: true}
		}
		to := c
		for range n {
			to = b.nextWord(to, big)
		}
		if op != 0 {
			// An operator stops at the end of the line the motion leaves.
			if i := lastIndex(b.r[c:to], '\n'); i >= 0 && c+i > c {
				to = c + i
			}
		} else {
			to = b.normalCol(to)
		}
		return vimMotion{to: to, ok: true}
	case 'e', 'E':
		to := c
		for range n {
			to = b.wordEnd(to, big)
		}
		return vimMotion{to: to, inclusive: true, ok: true}
	case 'b', 'B':
		to := c
		for range n {
			to = b.wordBack(to, big)
		}
		return vimMotion{to: to, ok: true}
	case '0':
		return vimMotion{to: b.lineStart(c), ok: true}
	case '^':
		return vimMotion{to: b.firstNonBlank(c), ok: true}
	case '$':
		to := c
		for range n - 1 {
			if next, ok := b.lineDown(to); ok {
				to = next
			}
		}
		to = b.lineEnd(to)
		if op == 0 {
			to = b.normalCol(to)
		}
		return vimMotion{to: to, ok: true}
	case 'G', 'g':
		line := -1 // the last line
		if key[0] == 'g' {
			line = 0
		}
		if count > 0 {
			line = count - 1
		}
		return vimMotion{to: b.lineAt(line), linewise: true, ok: true}
	case 'f', 'F', 't', 'T':
		v.find = slices.Clone(key)
		return b.findChar(c, key[0], key[1], n, false)
	case ';', ',':
		if len(v.find) != 2 {
			return vimMotion{}
		}
		kind := v.find[0]
		if key[0] == ',' {
			kind = map[rune]rune{'f': 'F', 'F': 'f', 't': 'T', 'T': 't'}[kind]
		}
		return b.findChar(c, kind, v.find[1], n, true)
	}
	return vimMotion{}
}

// findChar is f, F, t or T for ch, n times, within the line. A repeat
// (; and ,) of t or T looks past the match it stands next to.
func (b *vimBuf) findChar(c int, kind, ch rune, n int, repeat bool) vimMotion {
	ls, le := b.lineStart(c), b.lineEnd(c)
	p := c
	for i := range n {
		q := p
		switch kind {
		case 'f', 't':
			q++
			if kind == 't' && i == 0 && repeat {
				q++
			}
			for q < le && b.r[q] != ch {
				q++
			}
			if q >= le {
				return vimMotion{}
			}
		default:
			q--
			if kind == 'T' && i == 0 && repeat {
				q--
			}
			for q >= ls && b.r[q] != ch {
				q--
			}
			if q < ls {
				return vimMotion{}
			}
		}
		p = q
	}
	switch kind {
	case 'f':
		return vimMotion{to: p, inclusive: true, ok: true}
	case 't':
		return vimMotion{to: p - 1, inclusive: true, ok: true}
	case 'T':
		return vimMotion{to: p + 1, ok: true}
	}
	return vimMotion{to: p, ok: true}
}

// Text objects.

// object finds the range [s, e) of text object obj (w, W, a quote, or a
// bracket) at c; around includes the delimiters or trailing space.
func (b *vimBuf) object(c int, obj rune, around bool) (s, e int, ok bool) {
	switch obj {
	case 'w', 'W':
		return b.wordObject(c, obj == 'W', around)
	case '"', '\'', '`':
		return b.quoteObject(c, obj, around)
	}
	for _, p := range []string{"()b", "[]", "{}B", "<>"} {
		if strings.ContainsRune(p, obj) {
			return b.bracketObject(c, rune(p[0]), rune(p[1]), around)
		}
	}
	return 0, 0, false
}

func (b *vimBuf) wordObject(c int, big, around bool) (s, e int, ok bool) {
	ls, le := b.lineStart(c), b.lineEnd(c)
	if c >= le {
		return 0, 0, false
	}
	cls := vimClass(b.r[c], big)
	s, e = c, c+1
	for s > ls && vimClass(b.r[s-1], big) == cls {
		s--
	}
	for e < le && vimClass(b.r[e], big) == cls {
		e++
	}
	if !around {
		return s, e, true
	}
	if cls == 0 { // on spaces: they and the word after them
		if e < le {
			next := vimClass(b.r[e], big)
			for e < le && vimClass(b.r[e], big) == next {
				e++
			}
		}
		return s, e, true
	}
	t := e
	for t < le && b.r[t] == ' ' || t < le && b.r[t] == '\t' {
		t++
	}
	if t > e {
		return s, t, true
	}
	for s > ls && (b.r[s-1] == ' ' || b.r[s-1] == '\t') {
		s--
	}
	return s, e, true
}

func (b *vimBuf) quoteObject(c int, q rune, around bool) (s, e int, ok bool) {
	ls, le := b.lineStart(c), b.lineEnd(c)
	var at []int
	for i := ls; i < le; i++ {
		if b.r[i] == q && (i == ls || b.r[i-1] != '\\') {
			at = append(at, i)
		}
	}
	for i := 0; i+1 < len(at); i += 2 {
		open, closing := at[i], at[i+1]
		if c > closing {
			continue
		}
		if around {
			return open, closing + 1, true
		}
		return open + 1, closing, true
	}
	return 0, 0, false
}

func (b *vimBuf) bracketObject(c int, open, closing rune, around bool) (s, e int, ok bool) {
	s = -1
	depth := 0
	for i := min(c, len(b.r)-1); i >= 0; i-- {
		switch {
		case b.r[i] == closing && i != c:
			depth++
		case b.r[i] == open:
			if depth == 0 {
				s = i
			} else {
				depth--
			}
		}
		if s >= 0 {
			break
		}
	}
	if s < 0 {
		return 0, 0, false
	}
	depth = 0
	for i := s + 1; i < len(b.r); i++ {
		switch b.r[i] {
		case open:
			depth++
		case closing:
			if depth == 0 {
				if around {
					return s, i + 1, true
				}
				return s + 1, i, true
			}
			depth--
		}
	}
	return 0, 0, false
}

// Buffer helpers.

func vimClass(r rune, big bool) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case big, r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	}
	return 2
}

func (b *vimBuf) lineStart(p int) int {
	for p > 0 && b.r[p-1] != '\n' {
		p--
	}
	return p
}

func (b *vimBuf) lineEnd(p int) int {
	for p < len(b.r) && b.r[p] != '\n' {
		p++
	}
	return p
}

// normalCol clamps p onto a character, as normal mode's cursor is.
func (b *vimBuf) normalCol(p int) int {
	p = min(max(p, 0), len(b.r))
	ls, le := b.lineStart(p), b.lineEnd(p)
	if p >= le && le > ls {
		return le - 1
	}
	return p
}

func (b *vimBuf) firstNonBlank(p int) int {
	p = b.lineStart(p)
	le := b.lineEnd(p)
	for p < le && (b.r[p] == ' ' || b.r[p] == '\t') {
		p++
	}
	return p
}

// lineDown and lineUp move to the same column on the next or previous
// line; ok is false when there is none.
func (b *vimBuf) lineDown(p int) (int, bool) {
	le := b.lineEnd(p)
	if le >= len(b.r) {
		return p, false
	}
	col := p - b.lineStart(p)
	return min(le+1+col, b.lineEnd(le+1)), true
}

func (b *vimBuf) lineUp(p int) (int, bool) {
	ls := b.lineStart(p)
	if ls == 0 {
		return p, false
	}
	col := p - ls
	prev := b.lineStart(ls - 1)
	return min(prev+col, ls-1), true
}

// lineAt is the start of line n (from 0), or of the last line when n is
// negative or past the end.
func (b *vimBuf) lineAt(n int) int {
	p := 0
	for i := 0; n < 0 || i < n; i++ {
		le := b.lineEnd(p)
		if le >= len(b.r) {
			break
		}
		p = le + 1
	}
	return p
}

func (b *vimBuf) nextWord(p int, big bool) int {
	n := len(b.r)
	if p >= n {
		return n
	}
	if cls := vimClass(b.r[p], big); cls != 0 {
		for p < n && vimClass(b.r[p], big) == cls {
			p++
		}
	}
	for p < n && unicode.IsSpace(b.r[p]) {
		if b.r[p] == '\n' && p+1 < n && b.r[p+1] == '\n' {
			return p + 1 // an empty line counts as a word
		}
		p++
	}
	return p
}

func (b *vimBuf) atWordEnd(p int, big bool) bool {
	return p < len(b.r) && !unicode.IsSpace(b.r[p]) &&
		(p+1 >= len(b.r) || vimClass(b.r[p+1], big) != vimClass(b.r[p], big))
}

func (b *vimBuf) wordEnd(p int, big bool) int {
	n := len(b.r)
	p++
	for p < n && unicode.IsSpace(b.r[p]) {
		p++
	}
	if p >= n {
		return max(n-1, 0)
	}
	cls := vimClass(b.r[p], big)
	for p+1 < n && vimClass(b.r[p+1], big) == cls {
		p++
	}
	return p
}

func (b *vimBuf) wordBack(p int, big bool) int {
	if p <= 0 {
		return 0
	}
	p--
	for p > 0 && unicode.IsSpace(b.r[p]) {
		if b.r[p] == '\n' && b.r[p-1] == '\n' {
			return p // an empty line
		}
		p--
	}
	cls := vimClass(b.r[p], big)
	for p > 0 && vimClass(b.r[p-1], big) == cls {
		p--
	}
	return p
}

// join joins the cursor's line with the next, as J does.
func (b *vimBuf) join() {
	le := b.lineEnd(b.c)
	if le >= len(b.r) {
		return
	}
	e := le + 1
	for e < len(b.r) && (b.r[e] == ' ' || b.r[e] == '\t') {
		e++
	}
	sep := []rune(" ")
	if e >= len(b.r) || b.r[e] == '\n' || b.r[e] == ')' || le > 0 && b.r[le-1] == ' ' {
		sep = nil
	}
	b.delete(le, e)
	b.insert(le, sep)
	b.c = le
}

func (b *vimBuf) delete(a, z int) {
	b.r = append(b.r[:a:a], b.r[z:]...)
}

func (b *vimBuf) insert(at int, text []rune) {
	b.r = slices.Insert(b.r, at, text...)
}

func lastIndex(r []rune, ch rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == ch {
			return i
		}
	}
	return -1
}

// The composer side.

// vimAliases are non-character keys normal mode treats as commands.
var vimAliases = map[string]string{
	"left": "h", "right": "l", "home": "0", "end": "$", "backspace": "h", "delete": "x",
}

// vimKey gives a key to vim mode. ok is false when the composer should
// handle it as usual: typing in insert mode, and in normal mode the keys
// that aren't vim commands (enter, arrows up and down, ctrl+ shortcuts).
func (m *model) vimKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	v := m.vim
	k := msg.String()
	if v.insert {
		if k == "esc" {
			if !m.idle() && m.input.Value() == "" {
				return nil, false // nothing to edit, so esc interrupts
			}
			b := m.vimBuffer()
			v.escape(&b)
			m.setVimBuffer(b)
			return nil, true
		}
		v.record(k, msg.Text, m.keys.is(k, actNewline))
		return nil, false
	}
	if k == "esc" && len(v.pending) > 0 {
		v.pending = nil
		return nil, true
	}
	keys := msg.Text
	if alias, ok := vimAliases[k]; ok {
		keys = alias
	} else if keys == "" || msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) != 0 {
		v.pending = nil
		return nil, false
	}
	if keys == "?" && len(v.pending) == 0 && m.input.Value() == "" {
		return nil, false // the shortcuts
	}
	b := m.vimBuffer()
	hist := 0
	for _, r := range keys {
		hist = v.key(&b, r)
	}
	m.setVimBuffer(b)
	if hist < 0 || hist > 0 && m.histIdx >= 0 {
		m.recallHistory(hist)
	}
	return nil, true
}

// vimBuffer reads the prompt and cursor from the textarea.
func (m *model) vimBuffer() vimBuf {
	lines := strings.Split(m.input.Value(), "\n")
	c := 0
	for i := 0; i < m.input.Line() && i < len(lines); i++ {
		c += len([]rune(lines[i])) + 1
	}
	return vimBuf{r: []rune(m.input.Value()), c: c + m.input.Column()}
}

// setVimBuffer writes the prompt back, setting its text only when it
// changed, and places the cursor.
func (m *model) setVimBuffer(b vimBuf) {
	if text := string(b.r); text != m.input.Value() {
		m.input.SetValue(text)
	}
	row, col := 0, 0
	for _, r := range b.r[:min(b.c, len(b.r))] {
		if r == '\n' {
			row, col = row+1, 0
		} else {
			col++
		}
	}
	m.input.MoveToBegin()
	for i := 0; m.input.Line() < row && i < 10000; i++ {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(col)
}

// vimTag is the footer's mode label in vim mode.
func (m *model) vimTag() string {
	switch {
	case m.vim == nil:
		return ""
	case m.vim.insert:
		return " " + m.st.dim.Render("INSERT")
	}
	return " " + m.st.accent.Render("NORMAL")
}
