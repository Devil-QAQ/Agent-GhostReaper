// Shared by the actual input and textarea components, including searches and
// dialog fields. Keep native composition separate from submit shortcuts.
export function compositionInputHandlers(state, handlers) {
  return {
    onCompositionStart(event) {
      state.current = true;
      handlers.onCompositionStart?.(event);
    },
    onCompositionEnd(event) {
      state.current = false;
      handlers.onCompositionEnd?.(event);
    },
    onKeyDown(event) {
      const native = event.nativeEvent || event;
      if (state.current || event.isComposing || native.isComposing || event.keyCode === 229 || native.keyCode === 229) {
        // Stop enclosing dialog shortcuts and implicit form submission. Other
        // composing keys keep their native text-editing behavior.
        if (event.key === "Enter") event.preventDefault();
        event.stopPropagation();
        return;
      }
      handlers.onKeyDown?.(event);
    },
  };
}
