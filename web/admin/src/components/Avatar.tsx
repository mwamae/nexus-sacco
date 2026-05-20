// Initials-based avatar. Hue derived from the name so each user is stable.

type Size = 'sm' | 'md' | 'lg' | 'xl';

export function Avatar({ name = '', size = 'md' }: { name?: string; size?: Size }) {
  const initials = name
    .split(' ')
    .map((s) => s[0])
    .filter(Boolean)
    .slice(0, 2)
    .join('')
    .toUpperCase();
  const hue = ([...name].reduce((a, c) => a + c.charCodeAt(0), 0) * 47) % 360;
  const cls =
    size === 'sm' ? 'avatar avatar-sm' :
    size === 'lg' ? 'avatar avatar-lg' :
    size === 'xl' ? 'avatar avatar-xl' : 'avatar';
  return (
    <span
      className={cls}
      style={{
        background: `oklch(92% 0.04 ${hue})`,
        color: `oklch(35% 0.10 ${hue})`,
      }}
    >
      {initials || '?'}
    </span>
  );
}
