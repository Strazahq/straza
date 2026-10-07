// A hand-off between screens: the screen that sends puts what the next
// screen should open on (a user filter, a session, a record search), then
// navigates, and the next screen takes it once on mount. Module memory,
// never the address, so a reload lands on the plain screen.
const box: Record<string, unknown> = {};

export function put<T>(key: string, value: T) {
  box[key] = value;
}

export function take<T>(key: string): T | null {
  const v = box[key];
  delete box[key];
  return v === undefined ? null : (v as T);
}
