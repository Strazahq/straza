// The two ends of this tab's trip to a provider. A sign-in leaves in this
// tab and comes back to it, because the tab that finishes has to be signed
// in to Straza, and a tab keeps its own session across the trip. strazad's
// GET /v1/connect/callback redeems nothing: it sends the tab here with the
// provider's answer behind the # of the address.

// ComeBack is what the provider's return left behind the #: the code and
// the state of a sign-in to finish, or the provider's own refusal word.
export type ComeBack = { code: string; state: string } | { error: string };

const LEAD = "#connect&";

// takeComeBack reads the provider's answer once and wipes it from the
// address before anything else runs, so a reload, a copied address and the
// history never carry the code. It answers null when there is none.
export function takeComeBack(): ComeBack | null {
  const hash = window.location.hash;
  if (!hash.startsWith(LEAD)) return null;
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  const got = new URLSearchParams(hash.slice(LEAD.length));
  const error = got.get("error");
  if (error) return { error };
  const code = got.get("code");
  const state = got.get("state");
  return code && state ? { code, state } : null;
}

// leaveFor sends this tab to the provider's page. It is a function of its
// own so a suite can stand in for the navigation jsdom does not implement.
export function leaveFor(address: string) {
  window.location.assign(address);
}
