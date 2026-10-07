// StrazaMark renders the approved small compass-shield beside a product name.
export function StrazaMark({ className }: { className?: string }) {
  return (
    <svg width="24" height="24" viewBox="0 0 32 32" fill="none" className={className} aria-hidden="true" focusable="false" data-brand-mark>
      <path fill="#f6b63c" d="M7.4 5 H24.6 A1.4 1.4 0 0 1 26 6.4 V15.2 C26 22.1 21.7 26.9 16 29.1 C10.3 26.9 6 22.1 6 15.2 V6.4 A1.4 1.4 0 0 1 7.4 5 Z" />
      <path fill="#0b0d12" d="M16 6.9 L18.4 12.9 L24.4 15.3 L18.4 17.7 L16 23.7 L13.6 17.7 L7.6 15.3 L13.6 12.9 Z" />
    </svg>
  );
}
