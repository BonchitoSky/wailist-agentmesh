import Link from "next/link";
import { Logo } from "@/components/ui";

const NOT_FOUND_CSS = `
.not-found-container {
  min-height: 100vh;
  min-height: 100dvh;
}
`;

export default function NotFound() {
  return (
    <div className="not-found-container" style={containerStyle}>
      <style>{NOT_FOUND_CSS}</style>
      {/* Subtle glow background effect */}
      <div style={glowBgStyle} />
      
      {/* SVG canvas grid background element */}
      <div className="canvas-bg" style={gridBgStyle} />

      {/* Main card panel */}
      <div className="reveal" style={cardStyle}>
        {/* AgentMesh branding */}
        <div style={brandingStyle}>
          <Logo size={28} />
        </div>
        {/* Animated disconnected network node graphic */}
        <div style={graphicContainerStyle}>
          <svg width="120" height="120" viewBox="0 0 120 120" fill="none" aria-hidden="true">
            {/* Glowing outer dashed indicator */}
            <circle
              cx="60"
              cy="60"
              r="45"
              stroke="var(--accent)"
              strokeWidth="1"
              strokeDasharray="4 4"
              style={{
                opacity: 0.3,
                animation: "glow-pulse 4s ease-in-out infinite",
                transformOrigin: "center",
              }}
            />
            {/* Inner boundary ring */}
            <circle cx="60" cy="60" r="25" stroke="var(--border-strong)" strokeWidth="1" />
            
            {/* Disconnected central wallet hub */}
            <circle cx="60" cy="60" r="10" fill="var(--bg-elev-3)" stroke="var(--danger)" strokeWidth="2" />
            
            {/* Detached peripheral nodes */}
            <circle cx="25" cy="35" r="5" fill="var(--fg-dim)" />
            <circle cx="95" cy="35" r="5" fill="var(--fg-dim)" />
            <circle cx="60" cy="95" r="5" fill="var(--fg-dim)" />
            
            {/* Dotted broken connection lines */}
            <line x1="25" y1="35" x2="60" y2="60" stroke="var(--border-strong)" strokeWidth="1.5" strokeDasharray="3 3" />
            <line x1="95" y1="35" x2="60" y2="60" stroke="var(--border-strong)" strokeWidth="1.5" strokeDasharray="3 3" />
            <line x1="60" y1="95" x2="60" y2="60" stroke="var(--border-strong)" strokeWidth="1.5" strokeDasharray="3 3" />
            
            {/* Internal caution sign */}
            <path d="M57 52 L63 52 L61 58 Z" fill="var(--danger)" />
            <circle cx="60" cy="62" r="0.8" fill="var(--danger)" />
          </svg>
        </div>

        {/* 404 error code */}
        <div style={errorCodeStyle}>404</div>

        {/* Heading */}
        <h1 style={headingStyle}>Connection Lost</h1>
        
        {/* Description */}
        <p style={descriptionStyle}>
          The endpoint you are trying to reach doesn&apos;t exist or has moved. Your agent&apos;s wallet remains secure, but we couldn&apos;t resolve this route.
        </p>

        {/* Action navigation links */}
        <div style={actionsStyle}>
          <Link href="/workflows" style={primaryButtonStyle}>
            Go to Workflows
          </Link>
          <Link href="/" style={secondaryButtonStyle}>
            Return Home
          </Link>
        </div>
      </div>
    </div>
  );
}

const containerStyle: React.CSSProperties = {
  display: "flex",
  flexDirection: "column",
  alignItems: "center",
  justifyContent: "center",
  background: "var(--bg)",
  color: "var(--fg)",
  fontFamily: "var(--font-sans)",
  position: "relative",
  overflow: "hidden",
  padding: "24px",
};

const glowBgStyle: React.CSSProperties = {
  position: "absolute",
  width: "400px",
  height: "400px",
  background: "var(--accent-glow)",
  filter: "blur(120px)",
  borderRadius: "50%",
  top: "50%",
  left: "50%",
  transform: "translate(-50%, -50%)",
  pointerEvents: "none",
  opacity: 0.15,
};

const gridBgStyle: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  backgroundSize: "20px 20px",
  opacity: 0.1,
  pointerEvents: "none",
};

const cardStyle: React.CSSProperties = {
  background: "var(--bg-elev-1)",
  border: "1px solid var(--border)",
  borderRadius: "var(--r-4)",
  padding: "48px 32px",
  maxWidth: "460px",
  width: "100%",
  textAlign: "center",
  boxShadow: "0 20px 48px rgba(0, 0, 0, 0.6)",
  zIndex: 1,
  display: "flex",
  flexDirection: "column",
  alignItems: "center",
};

const brandingStyle: React.CSSProperties = {
  marginBottom: "24px",
};

const graphicContainerStyle: React.CSSProperties = {
  marginBottom: "24px",
  animation: "float-y 6s ease-in-out infinite",
};

const errorCodeStyle: React.CSSProperties = {
  fontFamily: "var(--font-mono)",
  fontSize: "72px",
  fontWeight: 700,
  letterSpacing: "-0.04em",
  lineHeight: 1,
  color: "var(--danger)",
  marginBottom: "8px",
};

const headingStyle: React.CSSProperties = {
  fontSize: "32px",
  fontWeight: 600,
  letterSpacing: "-0.02em",
  margin: "0 0 12px 0",
};

const descriptionStyle: React.CSSProperties = {
  fontSize: "14px",
  lineHeight: "1.6",
  color: "var(--fg-muted)",
  margin: "0 0 32px 0",
};

const actionsStyle: React.CSSProperties = {
  display: "flex",
  gap: "12px",
  width: "100%",
};

const primaryButtonStyle: React.CSSProperties = {
  flex: 1,
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  height: "40px",
  background: "var(--accent)",
  color: "var(--accent-fg)",
  border: "none",
  borderRadius: "var(--r-2)",
  fontSize: "13.5px",
  fontWeight: 600,
  textDecoration: "none",
  transition: "background 0.15s var(--ease)",
  cursor: "pointer",
};

const secondaryButtonStyle: React.CSSProperties = {
  flex: 1,
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  height: "40px",
  background: "transparent",
  color: "var(--fg)",
  border: "1px solid var(--border-strong)",
  borderRadius: "var(--r-2)",
  fontSize: "13.5px",
  fontWeight: 600,
  textDecoration: "none",
  transition: "background 0.15s var(--ease), border-color 0.15s var(--ease)",
  cursor: "pointer",
};


