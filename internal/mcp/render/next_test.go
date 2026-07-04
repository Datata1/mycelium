package render

import "testing"

func TestDetectDefinition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line string
		name string
		ok   bool
	}{
		{`func Greet() string {`, "Greet", true},
		{`func (s *AuthService) Login(ctx context.Context) error {`, "Login", true},
		{`type Walker struct {`, "Walker", true},
		{`type Watcher interface {`, "Watcher", true},
		{`def dequeue(self):`, "dequeue", true},
		{`    async def fetch(self, url):`, "fetch", true},
		{`class JobQueue:`, "JobQueue", true},
		{`export class AuthService {`, "AuthService", true},
		{`export default class App extends Component {`, "App", true},
		{`export interface SessionMeta {`, "SessionMeta", true},
		{`export type PlanTier = "free" | "pro"`, "PlanTier", true},
		{`export async function mintSession(u: User) {`, "mintSession", true},
		{`function helper() {`, "helper", true},

		// Call sites and plain text must NOT trigger the note.
		{`	s.Login(ctx, creds)`, "", false},
		{`return fmt.Errorf("login failed: %w", err)`, "", false},
		{`// Login authenticates a user.`, "", false},
		{`if err := q.dequeue(); err != nil {`, "", false},
		{`log.Printf("func %s done", name)`, "", false},
	}
	for _, tc := range cases {
		name, ok := detectDefinition(tc.line)
		if ok != tc.ok || name != tc.name {
			t.Errorf("detectDefinition(%q) = (%q, %v), want (%q, %v)",
				tc.line, name, ok, tc.name, tc.ok)
		}
	}
}

func TestLexicalDefinitionNote_FirstMatchWins(t *testing.T) {
	t.Parallel()
	note, ok := lexicalDefinitionNote([]string{
		`s.Login(ctx, creds)`,
		`func Greet() string {`,
		`func Farewell() string {`,
	})
	if !ok {
		t.Fatal("expected a note")
	}
	want := `note: "Greet" looks like a symbol definition — find_symbol("Greet") gives the definition; get_references("Greet") lists callers.`
	if note != want {
		t.Errorf("note = %q, want %q", note, want)
	}
	if _, ok := lexicalDefinitionNote([]string{`x := y + 1`}); ok {
		t.Error("no definition lines: expected no note")
	}
}
