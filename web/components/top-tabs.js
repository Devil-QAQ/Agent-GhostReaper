"use client";

// Shared by task space and capability library; each item navigates within its
// current section while the same segmented navigation stays above the content.
export function TopTabs({ tabs, value, onSelect, label, renderSuffix, ...props }) {
  return (
    <nav className="sticky top-14 z-30 -mx-2 bg-background/95 px-2 backdrop-blur" aria-label={label} {...props}>
      <div className="flex w-fit max-w-full flex-wrap gap-0.5 rounded-lg bg-muted p-1">
        {tabs.map((tab) => (
          <button
            type="button"
            key={tab.id}
            aria-current={value === tab.id ? "page" : undefined}
            className={
              "flex items-center rounded-md px-3 py-1 text-[13px] transition-colors duration-150 cursor-pointer " +
              (value === tab.id
                ? "bg-popover text-foreground shadow-xs"
                : "text-muted-foreground hover:bg-hover-lift hover:text-foreground")
            }
            onClick={() => onSelect(tab.id)}
          >
            {tab.title}
            {renderSuffix?.(tab)}
          </button>
        ))}
      </div>
    </nav>
  );
}
