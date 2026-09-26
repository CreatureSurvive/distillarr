// SPDX-License-Identifier: GPL-3.0-or-later

import { useEffect, useRef } from "react";

const storeKey = (key: string) => `scroll.${key}`;

export function savedScroll(key: string): number {
  try {
    return Number(sessionStorage.getItem(storeKey(key))) || 0;
  } catch {
    return 0;
  }
}

// Remembers a list view's window scroll and restores it once the list has
// rendered (ready). Cards link with plain #/ hrefs, so a back swipe and a
// forward click both arrive as POP; the position is restored on every mount.
// Saving waits for the restore so the short, still-loading page's clamped
// scroll can't overwrite the remembered position.
export function useScrollRestore(key: string, ready: boolean) {
  const restored = useRef(false);

  useEffect(() => {
    let raf = 0;
    const save = () => {
      if (!restored.current) return;
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => {
        try {
          sessionStorage.setItem(storeKey(key), String(Math.round(window.scrollY)));
        } catch {}
      });
    };
    window.addEventListener("scroll", save, { passive: true });
    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("scroll", save);
    };
  }, [key]);

  useEffect(() => {
    if (!ready || restored.current) return;
    const y = savedScroll(key);
    requestAnimationFrame(() => {
      if (y > 0) window.scrollTo(0, y);
      restored.current = true;
    });
  }, [ready, key]);
}
