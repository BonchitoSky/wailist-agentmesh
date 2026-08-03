"use client";
import { useState, useEffect, useCallback, useRef } from "react";
import { useRouter, usePathname } from "next/navigation";
import { Logo, Pill, Hairline, ghostBtnSm } from "@/components/ui";
import { useAuth } from "@/hooks/useAuth";

// Which chain settlements actually run on. Mainnet is the default because
// that is what the platform runs; overridable so a genuine testnet
// deployment doesn't have to lie in the other direction.
const ALGORAND_NETWORK = process.env.NEXT_PUBLIC_ALGORAND_NETWORK ?? "mainnet";

// Single source of truth for the primary routes. Rendered as pills in the bar on
// wide screens and as menu items inside the account panel once the bar collapses,
// so the two surfaces can never drift apart.
const NAV_ITEMS = [
  { label: "Workflows", href: "/workflows" },
  { label: "Usage", href: "/usage" },
  { label: "Credits", href: "/billing" },
] as const;

// Shared application top bar. Rendered identically on every authed page so the
// brand cluster, primary navigation, and account menu never drift between routes.
export function Topbar() {
  const router = useRouter();
  const pathname = usePathname();
  const { signOut, user } = useAuth();

  // Avatar initials from the signed-in email's local part (first two
  // alphanumerics, uppercased). Falls back to "AC" while auth is still loading.
  const initials =
    (user?.email ?? "")
      .split("@")[0]
      .replace(/[^a-zA-Z0-9]/g, "")
      .slice(0, 2)
      .toUpperCase() || "AC";

  // Account menu opens two ways: hovering with a mouse (soft — closes shortly
  // after the pointer leaves the avatar and panel) or clicking/tapping (pinned —
  // survives mouse-leave, closes on outside press, Escape, or item selection).
  // Touch pointers skip the hover path entirely, so mobile is tap-only.
  const [menuState, setMenuState] = useState<"closed" | "hover" | "pinned">(
    "closed",
  );
  const menuOpen = menuState !== "closed";
  const menuRef = useRef<HTMLDivElement>(null);
  const hoverCloseTimer = useRef<number | null>(null);

  const cancelHoverClose = useCallback(() => {
    if (hoverCloseTimer.current != null) {
      window.clearTimeout(hoverCloseTimer.current);
      hoverCloseTimer.current = null;
    }
  }, []);

  const onMenuPointerEnter = (e: React.PointerEvent) => {
    if (e.pointerType === "touch") return;
    cancelHoverClose();
    setMenuState((s) => (s === "closed" ? "hover" : s));
  };
  // Grace delay so a brief slip off the menu doesn't snap it shut.
  const onMenuPointerLeave = (e: React.PointerEvent) => {
    if (e.pointerType === "touch") return;
    cancelHoverClose();
    hoverCloseTimer.current = window.setTimeout(() => {
      setMenuState((s) => (s === "hover" ? "closed" : s));
    }, 160);
  };

  useEffect(() => cancelHoverClose, [cancelHoverClose]);

  // Below the nav breakpoint the menu is also the navigation, so a route change
  // has to dismiss it — otherwise the panel hangs over the page you just opened.
  useEffect(() => {
    setMenuState("closed");
  }, [pathname]);

  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMenuState("closed");
    };
    const onPointer = (e: PointerEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node))
        setMenuState("closed");
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPointer);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPointer);
    };
  }, [menuOpen]);

  const handleSignOut = async () => {
    await signOut();
    router.push("/");
  };

  return (
    <div
      className="tb"
      style={{
        height: 56,
        flexShrink: 0,
        background: "var(--bg-elev-1)",
        borderBottom: "1px solid var(--border)",
        display: "flex",
        alignItems: "center",
        gap: 20,
      }}
    >
      {/* Brand + workspace context — one visual group */}
      <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
        <button
          onClick={() => router.push("/")}
          style={{
            background: "transparent",
            border: "none",
            cursor: "pointer",
            padding: 0,
          }}
        >
          <Logo size={18} />
        </button>
        <Hairline vertical length={22} />
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          {/* Workspace switcher drops out on narrow screens — it is the widest
              element in the cluster and the least load-bearing. */}
          <button className="hide-md" style={ghostBtnSm}>
            Acme Capital ▾
          </button>
          <Pill mono dot tone="warm">
            {ALGORAND_NETWORK}
          </Pill>
        </div>
      </div>
      <div style={{ flex: 1 }} />
      {/* Navigation + account — the other group */}
      <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
        {/* `display` lives in .tb-nav (globals.css) so the breakpoint can drop it
            without fighting an inline style. */}
        <nav className="tb-nav" aria-label="Primary">
          {NAV_ITEMS.map(({ label, href }) => (
            <NavLink
              key={href}
              label={label}
              active={pathname.startsWith(href)}
              onClick={() => router.push(href)}
            />
          ))}
        </nav>
        <Hairline className="hide-md" vertical length={22} />
        <div
          className="profile-menu"
          ref={menuRef}
          onPointerEnter={onMenuPointerEnter}
          onPointerLeave={onMenuPointerLeave}
        >
          <button
            className="profile-menu__trigger"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label="Account menu"
            onClick={() =>
              setMenuState((s) => (s === "pinned" ? "closed" : "pinned"))
            }
          >
            {initials}
          </button>
          {menuOpen && (
            <div className="profile-menu__panel" role="menu">
              <div className="profile-menu__card">
                <div
                  style={{
                    padding: "12px 14px",
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                  }}
                >
                  <div
                    style={{
                      width: 28,
                      height: 28,
                      borderRadius: 999,
                      background: "var(--accent)",
                      color: "var(--accent-fg)",
                      display: "inline-flex",
                      alignItems: "center",
                      justifyContent: "center",
                      fontSize: 11,
                      fontWeight: 700,
                      flexShrink: 0,
                    }}
                  >
                    {initials}
                  </div>
                  <div style={{ minWidth: 0 }}>
                    <div
                      style={{
                        fontSize: 13,
                        fontWeight: 600,
                        color: "var(--fg)",
                      }}
                    >
                      Acme Capital
                    </div>
                    <div
                      style={{
                        fontSize: 11,
                        color: "var(--fg-dim)",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {user?.email ?? "—"}
                    </div>
                  </div>
                </div>
                <div className="profile-menu__divider" />
                {/* The bar's nav is hidden below the breakpoint, so the routes
                    surface here instead — one overflow surface, not a second
                    menu component with its own open/close state. */}
                <div className="show-md">
                  {NAV_ITEMS.map(({ label, href }) => (
                    <button
                      key={href}
                      className="profile-menu__item"
                      role="menuitem"
                      aria-current={
                        pathname.startsWith(href) ? "page" : undefined
                      }
                      onClick={() => {
                        setMenuState("closed");
                        router.push(href);
                      }}
                    >
                      {label}
                    </button>
                  ))}
                  <div className="profile-menu__divider" />
                </div>
                <button
                  className="profile-menu__item"
                  role="menuitem"
                  onClick={() => setMenuState("closed")}
                >
                  Settings
                </button>
                <div className="profile-menu__divider" />
                <button
                  className="profile-menu__item profile-menu__item--danger"
                  role="menuitem"
                  onClick={() => {
                    setMenuState("closed");
                    handleSignOut();
                  }}
                >
                  Sign out
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// Top-bar navigation link. Active route is filled + full-contrast; others are
// muted and lighten on hover, so the bar always signals "you are here".
function NavLink({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      onMouseEnter={(e) => {
        if (!active) e.currentTarget.style.background = "var(--bg-elev-2)";
      }}
      onMouseLeave={(e) => {
        if (!active) e.currentTarget.style.background = "transparent";
      }}
      style={{
        height: 28,
        padding: "0 12px",
        fontSize: 12.5,
        fontWeight: 500,
        background: active ? "var(--bg-elev-3)" : "transparent",
        border: "none",
        borderRadius: "var(--r-2)",
        color: active ? "var(--fg)" : "var(--fg-muted)",
        cursor: "pointer",
        fontFamily: "var(--font-sans)",
        display: "inline-flex",
        alignItems: "center",
        gap: 6,
        transition: "background .15s var(--ease), color .15s var(--ease)",
      }}
    >
      {label}
    </button>
  );
}
