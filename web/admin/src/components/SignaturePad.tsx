// Pointer-driven signature pad. Returns a PNG blob via getBlob().
// Use ref.current?.getBlob() at submit time.

import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react';

export type SignaturePadHandle = {
  /** PNG blob of the current drawing, or null when canvas is empty. */
  getBlob: () => Promise<Blob | null>;
  clear: () => void;
  isEmpty: () => boolean;
};

type Props = {
  width?: number;
  height?: number;
};

export const SignaturePad = forwardRef<SignaturePadHandle, Props>(function SignaturePad(
  { width = 480, height = 160 },
  ref,
) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const drawingRef = useRef(false);
  const lastRef = useRef<{ x: number; y: number } | null>(null);
  const [empty, setEmpty] = useState(true);

  const ctxOf = useCallback(() => {
    const c = canvasRef.current;
    return c?.getContext('2d') ?? null;
  }, []);

  useEffect(() => {
    const c = canvasRef.current;
    if (!c) return;
    // Match canvas pixel ratio for crisp lines on retina.
    const dpr = window.devicePixelRatio || 1;
    c.width = width * dpr;
    c.height = height * dpr;
    c.style.width = width + 'px';
    c.style.height = height + 'px';
    const ctx = c.getContext('2d');
    if (ctx) {
      ctx.scale(dpr, dpr);
      ctx.lineWidth = 2;
      ctx.lineCap = 'round';
      ctx.lineJoin = 'round';
      ctx.strokeStyle = '#29261b';
    }
  }, [width, height]);

  function point(e: React.PointerEvent<HTMLCanvasElement>) {
    const c = canvasRef.current!;
    const r = c.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  const onDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    e.preventDefault();
    canvasRef.current?.setPointerCapture(e.pointerId);
    drawingRef.current = true;
    lastRef.current = point(e);
    const ctx = ctxOf();
    if (ctx && lastRef.current) {
      // Tiny dot for a tap, in case the user lifts immediately.
      ctx.beginPath();
      ctx.arc(lastRef.current.x, lastRef.current.y, 1, 0, Math.PI * 2);
      ctx.fillStyle = '#29261b';
      ctx.fill();
    }
    if (empty) setEmpty(false);
  };

  const onMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!drawingRef.current) return;
    const ctx = ctxOf();
    const p = point(e);
    if (ctx && lastRef.current) {
      ctx.beginPath();
      ctx.moveTo(lastRef.current.x, lastRef.current.y);
      ctx.lineTo(p.x, p.y);
      ctx.stroke();
    }
    lastRef.current = p;
  };

  const onUp = (e: React.PointerEvent<HTMLCanvasElement>) => {
    drawingRef.current = false;
    lastRef.current = null;
    canvasRef.current?.releasePointerCapture(e.pointerId);
  };

  useImperativeHandle(ref, () => ({
    getBlob: () =>
      new Promise<Blob | null>((resolve) => {
        const c = canvasRef.current;
        if (!c || empty) {
          resolve(null);
          return;
        }
        c.toBlob((b) => resolve(b), 'image/png');
      }),
    clear: () => {
      const c = canvasRef.current;
      if (!c) return;
      const ctx = c.getContext('2d');
      if (ctx) {
        // Reset transform first so we clear the full backing buffer.
        ctx.save();
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.clearRect(0, 0, c.width, c.height);
        ctx.restore();
      }
      setEmpty(true);
    },
    isEmpty: () => empty,
  }), [empty]);

  return (
    <div style={{
      border: '1px solid var(--border)',
      borderRadius: 'var(--r-md)',
      background: 'var(--surface)',
      width,
      maxWidth: '100%',
    }}>
      <canvas
        ref={canvasRef}
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={onUp}
        onPointerLeave={onUp}
        style={{
          display: 'block',
          touchAction: 'none',
          cursor: 'crosshair',
          background: 'repeating-linear-gradient(135deg, var(--surface), var(--surface) 8px, var(--surface-2) 8px, var(--surface-2) 16px)',
          borderRadius: 'var(--r-md)',
        }}
      />
    </div>
  );
});
