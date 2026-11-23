export const EventBus = (() => {
  const listeners = {};

  function on(event, fn) {
    (listeners[event] ??= []).push(fn);
  }

  function off(event, fn) {
    listeners[event] = (listeners[event] ?? []).filter(f => f !== fn);
  }

  function emit(event, data) {
    (listeners[event] ?? []).forEach(fn => fn(data));
  }

  return { on, off, emit };
})();
