/* Sitzungs-Umgebung aus Testläufen heraushalten (Session-QA #612, 2026-09-30).

   PROBLEM
   Seit Karte #611 setzt der SessionStart-Hook `ABOARD_SESSION` über
   `CLAUDE_ENV_FILE` in jeder Claude-Sitzung (F-1358: gemessen, wirkt). Ein
   `bun run test` aus einer solchen Sitzung vererbt die Variable an jedes
   Werkzeug, das ein Test per spawn startet. `aboard-op.mjs card.claim` ohne
   `--session` fand dann die Sitzungskennung und endete mit 0 statt 2 — der
   Test „ohne Sitzung: Exit 2“ war rot, obwohl der Code stimmte. Umgekehrt
   hätte ein Test mit gesetzter Variable grün sein können, ohne das Verhalten
   zu prüfen, das er behauptet.

   LÖSUNG
   Die Variablen, die Werkzeuge und Hooks aus der Umgebung der Sitzung lesen,
   werden vor jeder Testdatei entfernt. Kindprozesse erben `process.env` —
   ein zentrales Entfernen trifft damit jeden spawn, auch künftige. Tests, die
   eine dieser Variablen brauchen, setzen sie ausdrücklich im `env` ihres
   spawn-Aufrufs (Beispiel: tests/aboard-op-cli.test.js „ABOARD_SESSION
   ersetzt --session“). Die Token-Variablen stehen mit auf der Liste, damit
   kein Testlauf je das echte Agenten-Token einer Sitzung zu Gesicht bekommt. */
const SITZUNGS_VARIABLEN = [
  'ABOARD_SESSION', 'ABOARD_HOOK_SESSION', 'ABOARD_PERSON', 'ABOARD_SYNC_PATH',
  'ABOARD_AGENT_TOKEN', 'ABOARD_AGENT_TOKEN_FILE', 'ABOARD_AGENT_TOKEN_ZIEL',
];
for (const name of SITZUNGS_VARIABLEN) delete process.env[name];
