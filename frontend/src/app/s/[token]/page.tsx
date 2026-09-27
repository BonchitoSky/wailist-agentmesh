import { SharePreview } from "@/components/share/SharePreview";

// The public end of a share link.
//
// /s sits OUTSIDE the PROTECTED prefixes in middleware.ts, deliberately and
// not by accident: every other workflow route bounces a signed-out visitor to
// /signin, which would make a link posted anywhere public show a stranger
// nothing but a login wall. Adding /s to that list would silently break the
// feature, so if you are here to do that, read SharePreview first.
//
// The native shell ships a static export and cannot author anything at all
// (lib/readonly.ts treats it as read-only unconditionally), so it gets no page
// for this route rather than a shell it could never use.
export function generateStaticParams() {
  return [];
}

export default async function SharePageRoute({
  params,
}: {
  params: Promise<{ token: string }>;
}) {
  const { token } = await params;
  // No Suspense boundary here, unlike /workflows/[id]: that one wraps a tree
  // that calls useSearchParams(), which Next 16 refuses to build without one.
  // SharePreview reads nothing from the query string, so a boundary would be
  // ceremony. Add one the moment it does.
  return <SharePreview token={token} />;
}
