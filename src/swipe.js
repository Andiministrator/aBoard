/* Swipe-to-Dismiss für Bottom Sheets auf Mobile (F-95) */
export function addSwipeToDismiss(sheetEl, onDismiss) {
  let startY = 0, deltaY = 0, active = false;
  sheetEl.addEventListener('touchstart', e => {
    /* Nur starten wenn Sheet-Inhalt nicht gescrollt ist (F-96) */
    const scrollable = sheetEl.querySelector('.modal-body, .desc-pane, .comments-list, .modal-desc-section');
    if (scrollable && scrollable.scrollTop > 0) return;
    startY = e.touches[0].clientY; deltaY = 0; active = true;
    sheetEl.style.transition = 'none';
  }, { passive: true });
  sheetEl.addEventListener('touchmove', e => {
    if (!active) return;
    deltaY = Math.max(0, e.touches[0].clientY - startY);
    sheetEl.style.transform = `translateY(${deltaY}px)`;
  }, { passive: true });
  function end() {
    if (!active) return; active = false;
    sheetEl.style.transition = 'transform 0.2s ease';
    if (deltaY > 100) { sheetEl.style.transform = 'translateY(100%)'; setTimeout(() => { if (sheetEl.isConnected) onDismiss(); }, 180); }
    else sheetEl.style.transform = '';
  }
  sheetEl.addEventListener('touchend',    end);
  sheetEl.addEventListener('touchcancel', end); /* Systemunterbrechungen (Anruf, iOS-Geste) */
}
