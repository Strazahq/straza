// StrazaMark renders the compass-shield beside a product name. The
// four-blade form is for 24 px and smaller; full draws the eight-point rose
// of the full mark, for larger sizes.
export function StrazaMark({ className, full }: { className?: string; full?: boolean }) {
  return (
    <svg width="24" height="24" viewBox="0 0 32 32" fill="none" className={className} aria-hidden="true" focusable="false" data-brand-mark>
      <path fill="#f6b63c" d="M7.4 5 H24.6 A1.4 1.4 0 0 1 26 6.4 V15.2 C26 22.1 21.7 26.9 16 29.1 C10.3 26.9 6 22.1 6 15.2 V6.4 A1.4 1.4 0 0 1 7.4 5 Z" />
      {full
        ? <path fill="#0b0d12" d="M16 7 L17.11 12.52 L19.68 11.52 L18.68 14.09 L24.2 15.2 L18.68 16.31 L19.68 18.88 L17.11 17.88 L16 23.4 L14.89 17.88 L12.32 18.88 L13.32 16.31 L7.8 15.2 L13.32 14.09 L12.32 11.52 L14.89 12.52 Z" />
        : <path fill="#0b0d12" d="M16 6.9 L18.4 12.9 L24.4 15.3 L18.4 17.7 L16 23.7 L13.6 17.7 L7.6 15.3 L13.6 12.9 Z" />}
    </svg>
  );
}
