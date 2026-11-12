import { ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";

const selectionWatchers = new Set<() => void>();
function selectionChanged() { for (const watch of selectionWatchers) watch(); }
function watchSelection(watch: () => void) {
  if (!selectionWatchers.size) {
    document.addEventListener("selectionchange", selectionChanged);
    document.addEventListener("focusin", selectionChanged);
    document.addEventListener("toggle", selectionChanged, true);
  }
  selectionWatchers.add(watch);
  return () => {
    selectionWatchers.delete(watch);
    if (!selectionWatchers.size) {
      document.removeEventListener("selectionchange", selectionChanged);
      document.removeEventListener("focusin", selectionChanged);
      document.removeEventListener("toggle", selectionChanged, true);
    }
  };
}

// Keep the feed and its full history intact. Only off-screen React subtrees
// are released, with measured space retaining scroll position. Live actions,
// the newest rows, focus and text selections never get unmounted.
export function TimelineWindow({ id, retain, children }: { id: string; retain: boolean; children: () => ReactNode }) {
  const element = useRef<HTMLDivElement>(null);
  const height = useRef(0);
  const marginBottom = useRef("0px");
  const lockedRef = useRef(false);
  const [visible, setVisible] = useState(true);
  const [locked, setLocked] = useState(false);
  const enabled = typeof IntersectionObserver !== "undefined" && typeof ResizeObserver !== "undefined";
  const interacted = () => {
    const node = element.current, value = window.getSelection();
    return Boolean(node && (node.contains(document.activeElement) || node.querySelector('details[open]') || value && !value.isCollapsed && (node.contains(value.anchorNode) || node.contains(value.focusNode))));
  };

  useLayoutEffect(() => {
    const node = element.current;
    if (!node || !enabled || !visible && !retain && !locked) return;
    const measure = () => {
      height.current = node.getBoundingClientRect().height;
      // :empty no longer matches a mounted row after its body is released.
      // Preserve its outer spacing too, otherwise every transition shifts the feed.
      marginBottom.current = window.getComputedStyle(node).marginBottom;
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [enabled, visible, retain, locked]);

  useEffect(() => {
    const node = element.current;
    if (!node || !enabled) return;
    const root = node.closest(".chat");
    const observer = new IntersectionObserver(records => {
      const record = records.at(-1);
      if (record) {
        const keep = interacted(); lockedRef.current = keep; setLocked(keep);
        setVisible(record.isIntersecting);
      }
    }, { root, rootMargin: "1200px 0px" });
    observer.observe(node);
    const selection = () => {
      const keep = interacted();
      if (keep !== lockedRef.current) { lockedRef.current = keep; setLocked(keep); }
    };
    const stopSelection = watchSelection(selection);
    return () => { observer.disconnect(); stopSelection(); };
  }, [enabled]);

  const mounted = !enabled || retain || visible || locked;
  return <div ref={element} data-timeline-id={id} data-timeline-mounted={mounted ? "true" : "false"} style={mounted ? undefined : { height: height.current, marginBottom: marginBottom.current }}>
    {mounted ? children() : null}
  </div>;
}
