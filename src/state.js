import { EventBus } from './eventbus.js';

export const State = (() => {
  let current = null;
  let _dirty  = false; /* Nur in-memory — nicht persistieren */

  function set(newState) {
    current = newState;
    _dirty  = true;
    EventBus.emit('state:changed', current);
  }

  function get() {
    return current;
  }

  function isDirty() {
    return _dirty;
  }

  /* Wird nach Initial-Load und nach erfolgreichem Persist aufgerufen */
  function markClean() {
    _dirty = false;
    EventBus.emit('state:dirty-changed', false);
  }

  return { set, get, isDirty, markClean };
})();
