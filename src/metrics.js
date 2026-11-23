/* Metrics-Modul — Sprint-Velocity + Burnup-Auswertung (Karte #61)
 *
 * Auswertungsstrategie:
 *   Burnup (nicht Burndown): Zeigt akkumulierten erledigten Aufwand über Zeit.
 *   Burnup ist intuitiver — man sieht Fortschritt, nicht Restarbeit.
 *   Burndown wäre visuell negativer und erfordert eine feste Sprint-Baseline.
 *
 * Timeline-Strategie (kein Sprint-Start/-Ende-Datum verfügbar):
 *   X-Achse = card.updated der erledigten Karten, chronologisch sortiert.
 *   Älteste created-Zeit aller Sprint-Karten = virtueller Startpunkt (Effort=0).
 *   Neueste updated-Zeit einer erledigten Karte = Endpunkt.
 *
 * Erledigt-Definition (F-1085/#537): titel-basiert über `Links.isDoneColumnId()`
 *   (F-127) — dieselbe Quelle, die renderer.js/main.js schon nutzen. Vorher
 *   verglich dieses Modul hart gegen `columnId === 'col-erledigt'`; eine manuell
 *   angelegte oder umbenannte Erledigt-Spalte wurde dadurch anders bewertet als
 *   im Rest der App (F-126, belegt im Divergenz-Block in tests/metrics.test.js).
 *
 * Archivierte Karten: werden explizit ausgeschlossen (!card.archived).
 *
 * Effort-Semantik: Karten ohne effort-Feld werden ignoriert (nicht als 0 gezählt),
 *   da 0 die Velocity-Berechnung verfälscht.
 */

import { isDoneColumnId } from './links.js';

export const Metrics = (() => {

  /**
   * Berechnet Sprint-Metriken für einen Sprint.
   *
   * @param {object} sprint  - Sprint-Objekt mit `cards: Card[]`
   * @param {object} [board] - Board-Objekt (liefert `columnDefs` für `isDoneColumnId`).
   *   F-1085: Fehlt `board` (alter Aufrufer), fällt `isDoneColumnId` selbst auf den
   *   Vergleich `columnId === 'col-erledigt'` zurück (siehe dort) — exakt das
   *   bisherige Verhalten dieses Moduls. Kein Sonderfall hier nötig.
   * @returns {{
   *   totalEffort: number,    - Summe effort aller nicht-archivierten Karten mit effort
   *   doneEffort: number,     - Summe effort erledigter Karten (Erledigt-Spalte + qa)
   *   doneRatio: number,      - doneEffort / totalEffort (0..1), NaN wenn totalEffort=0
   *   cardCount: number,      - Gesamtzahl nicht-archivierter Karten
   *   doneCount: number,      - Anzahl erledigter Karten (Erledigt-Spalte + qa)
   *   hasEffortData: boolean, - true wenn mind. 1 Karte effort hat
   *   timelinePoints: Array<{t: number, effort: number}>, - Burnup-Punkte (ms-Timestamp, kumulierter Effort)
   * }}
   */
  function computeSprintMetrics(sprint, board) {
    /* ADR-0046: Nur echte Sprints liefern Metriken — Lanes/Backlog skippen.
       Neutrales Metrik-Objekt, damit Aufrufer (Renderer) ohne Sonderfall arbeiten kann. */
    if (sprint && sprint.kind && sprint.kind !== 'sprint') {
      return {
        totalEffort: 0, doneEffort: 0, doneRatio: NaN,
        cardCount: 0, doneCount: 0, hasEffortData: false, timelinePoints: [],
      };
    }
    /* ADR-0036: Sprint-Meta-Karten explizit ausschließen (nicht nur per effort=null-Check) */
    const cards = (sprint.cards || []).filter(c => !c.archived && c.kind !== 'sprint-meta');

    /* F-1085: Erledigt-Erkennung über die geteilte Titel-Semantik (F-127) statt
       eines eigenen Hardcodes — angleicht an Links.isDoneColumnId(), das
       renderer.js/main.js bereits nutzen. */
    const isDone = c => isDoneColumnId(board, c.columnId) && c.qa === true;

    /* Effort-Felder auswerten — nur Karten mit gesetztem effort. F-1817: nur ECHTE
       Zahlen zählen; ein String ("3") würde per `+` verkettet, nicht addiert. */
    const hatEffort = c => typeof c.effort === 'number' && Number.isFinite(c.effort) && c.effort > 0;
    const cardsWithEffort = cards.filter(hatEffort);
    const totalEffort = cardsWithEffort.reduce((sum, c) => sum + c.effort, 0);

    const doneCards = cards.filter(isDone);
    const doneCardsWithEffort = doneCards.filter(hatEffort);
    const doneEffort = doneCardsWithEffort.reduce((sum, c) => sum + c.effort, 0);

    const hasEffortData = cardsWithEffort.length > 0;
    const doneRatio = totalEffort > 0 ? doneEffort / totalEffort : NaN;

    /* Burnup-Timeline: akumulierter erledigter Aufwand über Zeit */
    const timelinePoints = buildTimelinePoints(cards, doneCardsWithEffort);

    return {
      totalEffort,
      doneEffort,
      doneRatio,
      cardCount: cards.length,
      doneCount: doneCards.length,
      hasEffortData,
      timelinePoints,
    };
  }

  /**
   * Baut Burnup-Zeitreihe auf.
   * Startpunkt: älteste created-Zeit aller Karten → Effort 0
   * Weitere Punkte: erledigte Karten sortiert nach updated
   */
  function buildTimelinePoints(allCards, doneCardsWithEffort) {
    if (doneCardsWithEffort.length === 0) return [];

    /* Früheste created-Zeit aller Sprint-Karten (= virtueller Sprint-Start) */
    let earliestTs = Infinity;
    allCards.forEach(c => {
      const ts = parseTs(c.created);
      if (ts && ts < earliestTs) earliestTs = ts;
    });

    /* Fallback: erste updated-Zeit einer erledigten Karte */
    if (!isFinite(earliestTs)) {
      const firstDone = [...doneCardsWithEffort]
        .sort((a, b) => parseTs(a.updated) - parseTs(b.updated))[0];
      earliestTs = parseTs(firstDone?.updated) || Date.now();
    }

    /* Punkte aufbauen: nach updated sortiert, kumulierter Effort */
    const sorted = [...doneCardsWithEffort].sort(
      (a, b) => (parseTs(a.updated) || 0) - (parseTs(b.updated) || 0)
    );

    const points = [{ t: earliestTs, effort: 0 }];
    let cumEffort = 0;
    sorted.forEach(c => {
      const ts = parseTs(c.updated);
      if (!ts) return;
      cumEffort += c.effort;
      /* Gleicher Timestamp: letzten Punkt überschreiben (kein Duplikat) */
      if (points.length > 0 && points[points.length - 1].t === ts) {
        points[points.length - 1].effort = cumEffort;
      } else {
        points.push({ t: ts, effort: cumEffort });
      }
    });

    return points;
  }

  /**
   * Parst einen Timestamp-String (ISO-8601 oder Date-String) zu ms.
   * Gibt null zurück wenn nicht parsbar.
   */
  function parseTs(str) {
    if (!str) return null;
    const d = new Date(str);
    return isNaN(d.getTime()) ? null : d.getTime();
  }

  /**
   * Baut ein SVG-Sparkline-Element (Burnup-Linie) für den Sprint-Header.
   *
   * @param {object} metrics  - Rückgabe von computeSprintMetrics()
   * @returns {SVGElement|null}  - null wenn keine Effort-Daten vorhanden
   */
  function buildSparklineSVG(metrics) {
    const { timelinePoints, totalEffort, hasEffortData } = metrics;
    if (!hasEffortData || timelinePoints.length < 2) return null;

    const W = 200;
    const H = 40;
    const PAD = 3;

    /* Skalierung */
    const minT  = timelinePoints[0].t;
    const maxT  = timelinePoints[timelinePoints.length - 1].t;
    const rangeT = maxT - minT || 1;
    const maxE  = totalEffort;

    function scaleX(t)  { return PAD + ((t - minT) / rangeT) * (W - 2 * PAD); }
    function scaleY(e)  { return H - PAD - (e / maxE) * (H - 2 * PAD); }

    const points = timelinePoints.map(p =>
      `${scaleX(p.t).toFixed(1)},${scaleY(p.effort).toFixed(1)}`
    ).join(' ');

    /* SVG via DOM-API (kein innerHTML für User-Content, ADR-0009) */
    const NS = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('width', W);
    svg.setAttribute('height', H);
    svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
    svg.setAttribute('aria-hidden', 'true');
    svg.classList.add('sparkline');

    /* Ziellinie (totalEffort) */
    const goalLine = document.createElementNS(NS, 'line');
    goalLine.setAttribute('x1', PAD);
    goalLine.setAttribute('y1', scaleY(maxE).toFixed(1));
    goalLine.setAttribute('x2', W - PAD);
    goalLine.setAttribute('y2', scaleY(maxE).toFixed(1));
    goalLine.classList.add('sparkline-goal');
    svg.appendChild(goalLine);

    /* Burnup-Linie */
    const polyline = document.createElementNS(NS, 'polyline');
    polyline.setAttribute('points', points);
    polyline.classList.add('sparkline-line');
    svg.appendChild(polyline);

    /* Endpunkt-Marker */
    const lastPt = timelinePoints[timelinePoints.length - 1];
    const dot = document.createElementNS(NS, 'circle');
    dot.setAttribute('cx', scaleX(lastPt.t).toFixed(1));
    dot.setAttribute('cy', scaleY(lastPt.effort).toFixed(1));
    dot.setAttribute('r', '3');
    dot.classList.add('sparkline-dot');
    svg.appendChild(dot);

    return svg;
  }

  return { computeSprintMetrics, buildSparklineSVG };
})();
