/**
 * rest-ops.js — Finding-Nummern im Server-Modus (Kritiker K2-2, Sprint 82;
 * ADR-0133 §1, Karte #666).
 *
 * Der Browser vergibt beim Quick-Add eine Finding-Nummer lokal
 * (`nextFindingNr`, src/main.js `card:quick-add`). Hat am Server inzwischen
 * jemand anders ein Finding angelegt, vergibt der Fold dort eine andere. Bis
 * das GET nach dem POST übernommen ist — und das wartet, solange das
 * Karten-Modal offen ist oder L Ungesichertes trägt (src/main.js
 * `restEingang`) —, rechnet der Browser gegen einen Bezug mit SEINER Nummer.
 * Jede Bearbeitung des Findings ging dann mit dieser Nummer hinaus und wurde
 * am Server als `finding-nr-gesperrt` abgelehnt.
 *
 * Seit K2-2 schickt `bereiteOpsVor` `finding.nr` nie mit: Die Nummer vergibt
 * der Fold, eine fehlende ergänzt er auf Bestand (src/ops.js
 * `setzeKartenFeld`, `card.upsert`-Zweig).
 *
 * Rein, ohne Build-Konstanten — läuft in allen Kontexten gleich.
 */
import { describe, it, expect } from 'vitest';
import { bereiteOpsVor, fuehreBezugNach } from '../src/rest-ops.js';
import { fold, makeOp } from '../src/ops.js';
import { nextFindingNr, FINDING_KIND } from '../src/finding.js';

const klon = o => JSON.parse(JSON.stringify(o));
const brett = karten => ({
  version: '2',
  board: {
    id: 'b', title: 'B', nextDisplayId: 10,
    columnDefs: [{ id: 'col-backlog', title: 'Backlog', color: null }],
    sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: karten }],
  },
});
const finding = (id, displayId, nr, extra = {}) => ({
  id, displayId, title: `Befund ${id}`, description: 'd', columnId: 'col-backlog', labels: [], comments: [],
  kind: FINDING_KIND, finding: { nr, typ: 'B', prioritaet: 'P2', status: 'offen' }, ...extra,
});
const karte = (s, id) => s.board.sprints[0].cards.find(c => c.id === id);

/* Am Server: gleicher ts für den Batch, seq = Position (wie stempleOps). */
function amServer(stand, ops) {
  const protokoll = [];
  const erg = fold(stand, ops.map((o, i) => ({ ...o, ts: '2026-10-05T00:00:00.000Z', seq: i })), protokoll);
  return { erg, protokoll };
}

/* Ausgangslage: Bezug B mit F-1. Am Server hat ein Agent inzwischen F-2
   angelegt. Lokal legt der Browser per Quick-Add ein Finding an und vergibt
   dafür — wie src/main.js — selbst die Nummer: auch 2. */
function lage() {
  const B = brett([finding('f1', 1, 1)]);
  const server0 = amServer(B, [makeOp('card.upsert', { sprintId: '__backlog__', card: finding('f2', 2, null) }, 'agent')]).erg;
  const L = klon(B);
  const neu = finding('f3', 3, nextFindingNr(L.board));
  L.board.sprints[0].cards.push(neu);
  return { B, server0, L };
}

describe('K2-2: lokal vergebene Finding-Nummer weicht von der des Servers ab', () => {
  it('Ausgangslage: lokal F-2, am Server F-3 — das Fenster existiert', () => {
    const { B, server0, L } = lage();
    expect(karte(L, 'f3').finding.nr).toBe(2);
    const { erg } = amServer(server0, bereiteOpsVor(B, L, 'Andi').ops);
    expect(karte(erg, 'f2').finding.nr).toBe(2);
    expect(karte(erg, 'f3').finding.nr).toBe(3);
  });

  it('die Neuanlage schickt keine nr mit (der Fold vergibt sie)', () => {
    const { B, L } = lage();
    const ops = bereiteOpsVor(B, L, 'Andi').ops;
    const up = ops.find(o => o.type === 'card.upsert' && o.payload.card.id === 'f3');
    expect(up).toBeTruthy();
    expect(up.payload.card.finding).not.toHaveProperty('nr');
    /* Der lokal nachgeführte Bezug trägt trotzdem dieselbe Nummer wie L. */
    expect(karte(fuehreBezugNach(B, ops), 'f3').finding.nr).toBe(2);
  });

  it('Bearbeitung vor der Übernahme (card.field finding): am Server angenommen, Nummer bleibt die des Servers', () => {
    const { B, server0, L } = lage();
    const server1 = amServer(server0, bereiteOpsVor(B, L, 'Andi').ops).erg;
    /* Bezug := L (storage-plugin.js speichereUeberOps, `s.bezug = S`); das GET
       nach dem POST ist noch nicht übernommen (Karten-Modal offen). */
    const L2 = klon(L);
    karte(L2, 'f3').finding.status = 'behoben';
    karte(L2, 'f3').finding.prioritaet = 'P1';
    const ops = bereiteOpsVor(L, L2, 'Andi').ops;
    expect(ops.map(o => o.type)).toContain('card.field');
    const { erg, protokoll } = amServer(server1, ops);
    expect(protokoll).toEqual([]);
    expect(karte(erg, 'f3').finding).toMatchObject({ nr: 3, status: 'behoben', prioritaet: 'P1' });
  });

  it('Bearbeitung vor der Übernahme mit Voll-Upsert (Feld entfernt): ebenfalls angenommen', () => {
    const { B, server0, L } = lage();
    const server1 = amServer(server0, bereiteOpsVor(B, L, 'Andi').ops).erg;
    const L2 = klon(L);
    delete karte(L2, 'f3').description;          /* entferntes Feld → Volltext-Upsert */
    karte(L2, 'f3').finding.status = 'behandelt';
    const ops = bereiteOpsVor(L, L2, 'Andi').ops;
    const up = ops.find(o => o.type === 'card.upsert');
    expect(up).toBeTruthy();
    expect(up.payload.card.finding).not.toHaveProperty('nr');
    const { erg, protokoll } = amServer(server1, ops);
    /* Gemeldet wird nur die gewollte Entkernung (F-926: `description` fehlt
       in der Payload) — keine Ablehnung. */
    expect(protokoll.filter(e => e.art === 'wirkungslos')).toEqual([]);
    expect(protokoll.map(e => e.art)).toEqual(['entkernung']);
    expect(karte(erg, 'f3').finding).toMatchObject({ nr: 3, status: 'behandelt' });
  });

  it('die Eingabe von bereiteOpsVor bleibt unverändert (nur die Ops verlieren nr)', () => {
    const { B, L } = lage();
    const vorher = JSON.stringify(L);
    bereiteOpsVor(B, L, 'Andi');
    expect(JSON.stringify(L)).toBe(vorher);
  });
});
