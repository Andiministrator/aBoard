/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/modal-shell.js (#348, ADR-0118).
 *
 * modal-shell.js ist NICHT hinter IS_SERVER_BUILD gegated (wie focus-trap.js,
 * dessen Verhalten es verdrahtet) — diese Suite läuft deshalb in ALLEN vier
 * Testkontexten, nicht nur den beiden Server-Kontexten. Das ist Absicht: die
 * Shell selbst ist ein allgemeiner Baustein, den auch ein zukünftiger
 * Nicht-Server-Dialog verwenden könnte.
 *
 * Wichtig (Klasse 32, Karte #348): Diese Suite testet NUR `createModal()`
 * selbst. Ob ein migrierter Dialog (clients-view.js, projects-view.js,
 * notifications-dialog.js) die Shell tatsächlich BENUTZT, prüfen die
 * jeweiligen dialogspezifischen Testdateien — ein grüner Test hier sagt
 * nichts darüber aus.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createModal } from '../src/modal-shell.js';

function baueModal(opts = {}) {
  return createModal({
    extraOverlayClass: 'test-overlay',
    extraPanelClass: 'test-panel',
    titleId: 'test-dialog-title',
    closeAriaLabel: 'Schließen',
    ...opts,
  });
}

describe('modal-shell.js — createModal() (#348)', () => {
  afterEach(() => { document.body.innerHTML = ''; });

  it('baut Overlay/Panel mit den Basis-Klassen aus style.css + dialogspezifischer Zusatzklasse', () => {
    const modal = baueModal();
    expect(modal.overlay.classList.contains('modal-overlay')).toBe(true);
    expect(modal.overlay.classList.contains('test-overlay')).toBe(true);
    expect(modal.panel.classList.contains('modal')).toBe(true);
    expect(modal.panel.classList.contains('test-panel')).toBe(true);
    expect(modal.panel.getAttribute('role')).toBe('dialog');
    expect(modal.panel.getAttribute('aria-modal')).toBe('true');
    expect(modal.panel.getAttribute('aria-labelledby')).toBe('test-dialog-title');
    expect(modal.titleEl.id).toBe('test-dialog-title');
    expect(modal.body.classList.contains('modal-body')).toBe(true);
  });

  it('extraBodyClass (Bestandsschutz Familie A): Body trägt zusätzlich die alte Klasse', () => {
    const modal = baueModal({ extraBodyClass: 'admin-body' });
    expect(modal.body.classList.contains('modal-body')).toBe(true);
    expect(modal.body.classList.contains('admin-body')).toBe(true);
  });

  it('ohne extraBodyClass trägt der Body nur modal-body', () => {
    const modal = baueModal();
    expect(modal.body.className.trim()).toBe('modal-body');
  });

  it('setTitle() setzt den Titeltext', () => {
    const modal = baueModal();
    modal.setTitle('Mein Dialog');
    expect(modal.titleEl.textContent).toBe('Mein Dialog');
  });

  it('open() hängt das Overlay an document.body und setzt aria-hidden=false + --open-Klasse', () => {
    const modal = baueModal();
    expect(document.body.contains(modal.overlay)).toBe(false);
    modal.open();
    expect(document.body.contains(modal.overlay)).toBe(true);
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('false');
    expect(modal.overlay.classList.contains('modal-overlay--open')).toBe(true);
  });

  /* F-754: open() setzte die --open-Klasse bisher im SELBEN Tick wie
     aria-hidden — der Browser rendert den geschlossenen Ausgangszustand
     (opacity:0/visibility:hidden, siehe style.css .modal-overlay) beim
     ALLERERSTEN Öffnen dann nie, die CSS-Transition greift nicht (Sprung
     statt Animation). Fix: erzwungener Reflow (`overlay.offsetHeight`
     lesen) VOR dem Klassenwechsel — SYNCHRON, kein requestAnimationFrame.
     Bewusst KEIN rAF (wie im Karten-Modal/Einstellungen-Dialog): alle
     sieben Konsumenten rufen focusAfterRender() über eine bereits
     aufgelöste Promise-.then()-Kette auf (Microtask) — die liefe vor einem
     rAF-Callback (Makrotask) und würde versuchen, ein noch
     visibility:hidden-Element zu fokussieren (stiller No-Op in echten
     Browsern). Dieser Test verankert die SYNCHRONE Variante: ein Rückbau
     auf das naheliegende rAF-Muster würde ihn reissen. */
  it('open() setzt die --open-Klasse SYNCHRON — kein requestAnimationFrame (Fokus-Race-Schutz, F-754)', () => {
    const rafSpy = vi.spyOn(globalThis, 'requestAnimationFrame');
    const modal = baueModal();
    modal.open();
    expect(modal.overlay.classList.contains('modal-overlay--open')).toBe(true);
    expect(rafSpy).not.toHaveBeenCalled();
    rafSpy.mockRestore();
  });

  it('close() setzt aria-hidden=true und entfernt die --open-Klasse', () => {
    const modal = baueModal();
    modal.open();
    modal.close();
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
    expect(modal.overlay.classList.contains('modal-overlay--open')).toBe(false);
  });

  it('ESC schließt den Dialog', () => {
    const modal = baueModal();
    modal.open();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
  });

  it('Klick auf das Overlay (außerhalb des Panels) schließt', () => {
    const modal = baueModal();
    modal.open();
    modal.overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    /* happy-dom setzt e.target nicht automatisch — wir simulieren den echten
       Klick-Listener-Vertrag direkt über ein Event, dessen target das Overlay
       selbst ist (identisch zum Produktivpfad `e.target === overlay`). */
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
  });

  it('Klick auf das Panel schließt NICHT', () => {
    const modal = baueModal();
    modal.open();
    modal.panel.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('false');
  });

  it('Klick auf den Close-Button schließt', () => {
    const modal = baueModal();
    modal.open();
    modal.closeBtn.click();
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
  });

  it('Focus-Trap: Tab am letzten fokussierbaren Element springt zum ersten', () => {
    const modal = baueModal();
    const input = document.createElement('input');
    modal.body.appendChild(input);
    modal.open();
    /* Reihenfolge im DOM: Close-Button (Header) zuerst, dann das Input im
       Body — letztes fokussierbares Element ist also das Input. */
    input.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(modal.closeBtn);
  });

  it('Focus-Trap: Shift+Tab am ersten fokussierbaren Element (Close-Button) springt zum letzten', () => {
    const modal = baueModal();
    const input = document.createElement('input');
    modal.body.appendChild(input);
    modal.open();
    modal.closeBtn.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(input);
  });

  it('Focus-Restore: close() gibt den Fokus an das vor dem Öffnen fokussierte Element zurück', () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();

    const modal = baueModal();
    modal.open();
    modal.focusAfterRender();
    expect(document.activeElement).not.toBe(opener);

    modal.close();
    expect(document.activeElement).toBe(opener);
  });

  it('focusAfterRender() fokussiert das erste Input im Body, falls vorhanden', () => {
    const modal = baueModal();
    const input = document.createElement('input');
    modal.body.appendChild(input);
    modal.open();
    modal.focusAfterRender();
    expect(document.activeElement).toBe(input);
  });

  it('focusAfterRender() fällt ohne Body-Inhalt auf den Close-Button zurück', () => {
    const modal = baueModal();
    modal.open();
    modal.focusAfterRender();
    expect(document.activeElement).toBe(modal.closeBtn);
  });

  /* F-752-Kritiker-Fund: Der frühere Test hier ("erneutes open() nach
     close() hängt das Overlay nicht doppelt an") prüfte per
     `document.querySelectorAll('.test-overlay').length === 1` genau die
     Zusicherung, die der `if (!overlay.parentNode)`-Guard in open()
     (src/modal-shell.js) angeblich schützt. Gemessen (Kritiker + Gegenprobe
     hier bestätigt): Guard entfernt → alle 22 Tests dieser Datei bleiben
     grün. Grund: `appendChild(x)` auf einen bereits an DIESEN Parent
     angehängten Knoten ist per DOM-Spezifikation ein reines Verschieben ans
     Ende — nie eine Duplizierung. Ob der Guard davor steht oder nicht, das
     `overlay`-Objekt bleibt derselbe eine DOM-Knoten, es gibt strukturell
     keinen Weg, per Element-Zähl-Assertion zwischen "Guard vorhanden" und
     "Guard fehlt" zu unterscheiden — auch ein Aufbau mit zwei
     `createModal()`-Instanzen und mehrfachem `open()` je Instanz landet bei
     derselben Element-Anzahl, mit oder ohne Guard. Deshalb ersatzlos
     gestrichen statt durch eine gleichermaßen nicht-diskriminierende
     Variante ersetzt (Alternative laut Karte geprüft und verworfen — kein
     rebuild verfügbar, der tatsächlich vom Guard abhinge). Der Guard selbst
     bleibt im Produktivcode (harmlos, dokumentiert seine Absicht), ist aber
     kein Fall für einen Regressionstest, den DOM-Semantik ohnehin
     garantiert. */

  it('footer:true liefert einen .modal-actions-Container, sonst null', () => {
    const mitFooter = baueModal({ footer: true });
    expect(mitFooter.footer).toBeTruthy();
    expect(mitFooter.footer.classList.contains('modal-actions')).toBe(true);
    expect(mitFooter.panel.contains(mitFooter.footer)).toBe(true);

    const ohneFooter = baueModal();
    expect(ohneFooter.footer).toBeNull();
  });

  it('closeOnOverlayClick:false — Klick auf den Hintergrund schließt NICHT', () => {
    const modal = baueModal({ closeOnOverlayClick: false });
    modal.open();
    modal.overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('false');
  });

  it('closeOnOverlayClick:false — Close-Button und ESC funktionieren trotzdem', () => {
    const modal = baueModal({ closeOnOverlayClick: false });
    modal.open();
    modal.closeBtn.click();
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
    modal.open();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
  });

  it('onRequestClose überschreibt ESC/Close-Button/Klick-außerhalb — eingebaute close() bleibt separat aufrufbar', () => {
    const onRequestClose = vi.fn();
    const modal = baueModal({ onRequestClose });
    modal.open();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    expect(onRequestClose).toHaveBeenCalledTimes(1);
    /* Die eingebaute close() wurde NICHT ausgelöst — Overlay bleibt offen. */
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('false');

    modal.closeBtn.click();
    expect(onRequestClose).toHaveBeenCalledTimes(2);

    modal.overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(onRequestClose).toHaveBeenCalledTimes(3);

    /* Aufrufer entscheidet selbst, wann tatsächlich geschlossen wird. */
    modal.close();
    expect(modal.overlay.getAttribute('aria-hidden')).toBe('true');
  });

  it('Variante "settings": zusätzliche Klassen modal-overlay--settings / modal--settings', () => {
    const modal = baueModal({ variant: 'settings' });
    expect(modal.overlay.classList.contains('modal-overlay--settings')).toBe(true);
    expect(modal.panel.classList.contains('modal--settings')).toBe(true);
  });

  it('ohne Variante-Angabe keine settings-Klassen', () => {
    const modal = baueModal();
    expect(modal.overlay.classList.contains('modal-overlay--settings')).toBe(false);
    expect(modal.panel.classList.contains('modal--settings')).toBe(false);
  });

  /* F-753-Kritiker-Fund: das einzige NEUE CSS der ganzen Karte #348
     (_ensureSettingsVariantStyles(), laut ADR-0118) war ungedeckt — kein
     Test prüfte, dass der <style data-aboard="modal-shell-settings-variant">
     überhaupt injiziert wird. Gemessen: Aufruf von
     _ensureSettingsVariantStyles() in createModal() entfernt → alle 22
     Shell-Tests + 91 Dialog-Tests blieben grün. Muster wie
     tests/projects-view.test.js:69 (cp-content-CSS): genau EIN <style>-Tag,
     auch bei mehreren variant:'settings'-Dialogen (der Injection-Guard
     `_settingsStylesInjected` ist Modul-weit, nicht pro Instanz). */
  /* F-889 (Karte #465, Sprint 63): createModal() hängt seinen ESC-Handler
     unconditional an `document` — bei zwei gleichzeitig offenen Shell-
     Instanzen feuern beide Listener und schliessen beide Dialoge. Die
     Reparatur ist eine Stapel-Regel (siehe modal-shell.js#onKey), keine
     Existenzprüfung — sonst Regress von F-752 (Kritiker-Fund, in modal.js
     dokumentiert). Diese Sektion deckt genau die Lücke, die die Karte
     benennt: tests/modal-shell.test.js prüfte bisher NIE zwei gleichzeitige
     Instanzen; tests/configdialog-modal-shell-441.test.js testet den
     Doppel-offen-Zustand nur für Close-Button-Klicks, nie für ESC. */
  describe('F-889: ESC bei zwei gleichzeitig offenen Shell-Instanzen', () => {
    it('schliesst NUR den zuletzt geöffneten (oberen) Dialog bei GLEICHEM Tier (Basis, z-index 100)', () => {
      const unten = baueModal({ extraOverlayClass: 'test-unten' });
      const oben  = baueModal({ extraOverlayClass: 'test-oben' });
      unten.open();
      oben.open();
      expect(unten.overlay.classList.contains('modal-overlay--open')).toBe(true);
      expect(oben.overlay.classList.contains('modal-overlay--open')).toBe(true);

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

      /* Der obere (später geöffnete, im DOM hintere) Dialog schliesst … */
      expect(oben.overlay.classList.contains('modal-overlay--open')).toBe(false);
      /* … der untere bleibt unangetastet — OHNE die Stapel-Regel schliessen
         hier BEIDE Listener (F-889-Regressionsfall). */
      expect(unten.overlay.classList.contains('modal-overlay--open')).toBe(true);

      unten.close();
    });

    it('Settings-Tier (z-index 11000) schliesst zuerst, auch wenn der Basis-Dialog (z-index 100) später geöffnet wurde', () => {
      const settings = baueModal({ extraOverlayClass: 'test-settings', variant: 'settings' });
      const basis     = baueModal({ extraOverlayClass: 'test-basis' });
      /* Bewusst in dieser Reihenfolge geöffnet — Settings ZUERST, Basis
         DANACH — um zu zeigen, dass der Tier-Vergleich nicht von der
         Öffnungs-/DOM-Reihenfolge abhängt, sondern vom z-index-Tier: die
         Settings-Variante liegt (ADR-0118 §4) IMMER visuell über der
         Basis-Variante, unabhängig davon, wer zuletzt geöffnet wurde. */
      settings.open();
      basis.open();

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

      expect(settings.overlay.classList.contains('modal-overlay--open')).toBe(false);
      expect(basis.overlay.classList.contains('modal-overlay--open')).toBe(true);

      basis.close();
    });

    it('nach dem Schliessen des oberen Dialogs schliesst ein zweiter ESC-Druck den verbleibenden unteren', () => {
      const unten = baueModal({ extraOverlayClass: 'test-unten-2' });
      const oben  = baueModal({ extraOverlayClass: 'test-oben-2' });
      unten.open();
      oben.open();

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      expect(oben.overlay.classList.contains('modal-overlay--open')).toBe(false);
      expect(unten.overlay.classList.contains('modal-overlay--open')).toBe(true);

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      expect(unten.overlay.classList.contains('modal-overlay--open')).toBe(false);
    });
  });

  it('Variante "settings" injiziert das Settings-Varianten-CSS genau EINMAL, auch bei mehreren Dialogen', () => {
    /* KEIN "vorher 0"-Check: `_settingsStylesInjected` ist ein Modul-weiter
       Singleton-Guard (wie ensureCpContentStyles() in clients-view.js) —
       ob VOR diesem Test schon ein anderer variant:'settings'-Dialog in
       DIESER Testdatei lief, hängt von der Ausführungsreihenfolge ab
       (siehe Zeile 236f. oben). Genau wie tests/projects-view.test.js:69
       wird deshalb nur die STABILE Zusage geprüft: nie mehr als ein
       Style-Tag, unabhängig davon, wie viele Settings-Dialoge geöffnet
       werden. */
    baueModal({ variant: 'settings', extraOverlayClass: 'test-overlay-a' });
    expect(document.querySelectorAll('style[data-aboard="modal-shell-settings-variant"]').length).toBe(1);
    baueModal({ variant: 'settings', extraOverlayClass: 'test-overlay-b' });
    expect(document.querySelectorAll('style[data-aboard="modal-shell-settings-variant"]').length).toBe(1);
  });

  /* Verify-Lauf zum Sprint-63-Abschluss: Der Fix „erst Stapel-Guard, dann
     preventDefault" hatte keinen Test. Ein nicht-oberster Dialog schluckte
     sonst die Vorgabeaktion von ESC (Vollbild verlassen, natives <dialog>,
     IME-Komposition abbrechen), obwohl er das Ereignis unbehandelt
     weiterreicht — und ein versehentliches Zurückdrehen der Reihenfolge wäre
     von keiner Zusicherung bemerkt worden. */
  describe('F-889: ESC wird nur abgefangen, wenn der Dialog auch zuständig ist', () => {
    it('der zustaendige (oberste) Dialog faengt ESC ab', () => {
      const unten = baueModal({ extraOverlayClass: 'test-pd-unten' });
      const oben  = baueModal({ extraOverlayClass: 'test-pd-oben' });
      unten.open();
      oben.open();

      const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
      document.dispatchEvent(ev);

      /* Der OBERE Dialog ist zuständig, schliesst und fängt ab. */
      expect(oben.overlay.classList.contains('modal-overlay--open')).toBe(false);
      expect(ev.defaultPrevented, 'der zuständige Dialog fängt ESC ab').toBe(true);

      unten.close();
    });

    it('ist gar kein Dialog zuständig, bleibt ESC unangetastet', () => {
      /* Ein Dialog, der nicht offen ist, hat keinen Listener mehr — aber ein
         offener, der NICHT oberstes Overlay ist, darf nicht abfangen. Dafür
         wird ein fremdes Overlay über ihn gelegt, das keine Shell-Instanz ist
         (also auch nicht auf ESC reagiert). */
      const dlg = baueModal({ extraOverlayClass: 'test-pd-allein' });
      dlg.open();

      const fremd = document.createElement('div');
      fremd.className = 'modal-overlay modal-overlay--open';
      document.body.appendChild(fremd);

      const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
      document.dispatchEvent(ev);

      expect(dlg.overlay.classList.contains('modal-overlay--open'),
        'der untere Dialog bleibt offen').toBe(true);
      expect(ev.defaultPrevented,
        'wer nicht schliesst, darf die Vorgabeaktion nicht schlucken').toBe(false);

      fremd.remove();
      dlg.close();
    });
  });
});
