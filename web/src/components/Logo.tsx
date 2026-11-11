// The Launchpad mark: a launch trajectory that rises from a pad, drawn with
// the brand gradient (pink to blue).
export default function Logo({ size = 26 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <defs>
        <linearGradient id="lp-brand" x1="0" y1="1" x2="1" y2="0">
          <stop offset="0" stopColor="#FF4FCB" />
          <stop offset="1" stopColor="#53B6FF" />
        </linearGradient>
      </defs>
      <rect x="3" y="25" width="12" height="3" rx="1.5" fill="#33285C" />
      <path d="M9 25 C 11 15, 17 8, 27 5" fill="none" stroke="url(#lp-brand)" strokeWidth="3" strokeLinecap="round" />
      <circle cx="27" cy="5" r="3.2" fill="#53B6FF" />
    </svg>
  );
}
