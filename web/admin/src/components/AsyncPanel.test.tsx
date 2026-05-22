import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

import { AsyncPanel } from './AsyncPanel';

beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }); });
afterEach(() => { vi.useRealTimers(); });

// Helper — a controllable promise so each test decides exactly when the
// fetcher settles. resolve/reject called once; subsequent calls no-op.
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe('<AsyncPanel>', () => {
  it('renders the data branch when the fetcher resolves', async () => {
    const d = deferred<string[]>();
    render(
      <AsyncPanel
        fetcher={() => d.promise}
        empty={<div>nothing yet</div>}
        errorMessage="something broke"
      >
        {(items) => <ul>{items.map((s) => <li key={s}>{s}</li>)}</ul>}
      </AsyncPanel>,
    );

    await act(async () => { d.resolve(['alpha', 'beta']); });

    expect(await screen.findByText('alpha')).toBeInTheDocument();
    expect(screen.getByText('beta')).toBeInTheDocument();
    expect(screen.queryByText('nothing yet')).not.toBeInTheDocument();
  });

  it('renders the typed empty branch when isEmpty(value) is true', async () => {
    const d = deferred<string[]>();
    render(
      <AsyncPanel
        fetcher={() => d.promise}
        isEmpty={(v) => v.length === 0}
        empty={<div data-testid="empty">No items here.</div>}
        errorMessage="should not see this"
      >
        {(items) => <div>has {items.length} items</div>}
      </AsyncPanel>,
    );

    await act(async () => { d.resolve([]); });

    expect(await screen.findByTestId('empty')).toHaveTextContent('No items here.');
  });

  it('renders the typed error branch with a Retry button on rejection', async () => {
    const d = deferred<string[]>();
    render(
      <AsyncPanel
        fetcher={() => d.promise}
        empty={<div>nothing</div>}
        errorMessage="Couldn't load the widgets."
      >
        {(items) => <div>{items.length}</div>}
      </AsyncPanel>,
    );

    await act(async () => { d.reject(new Error('500 from server')); });

    expect(await screen.findByText("Couldn't load the widgets.")).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled();
  });

  it('shows the skeleton once the skeletonDelay grace window elapses', async () => {
    const d = deferred<string[]>();
    render(
      <AsyncPanel
        fetcher={() => d.promise}
        skeletonDelayMs={50}
        skeleton={<div data-testid="skel">shimmer</div>}
        empty={<div>nothing</div>}
        errorMessage="oops"
      >
        {(items) => <div>{items.length}</div>}
      </AsyncPanel>,
    );

    // Before the delay, nothing renders — avoids skeleton-flash on fast fetches.
    expect(screen.queryByTestId('skel')).not.toBeInTheDocument();

    await act(async () => { vi.advanceTimersByTime(60); });

    expect(screen.getByTestId('skel')).toBeInTheDocument();

    // Resolving after the skeleton mounted swaps it for data.
    await act(async () => { d.resolve(['x']); });
    await waitFor(() => expect(screen.queryByTestId('skel')).not.toBeInTheDocument());
  });

  it('Retry re-invokes the fetcher and replaces the error with new data', async () => {
    // Fresh deferred per attempt so the test controls each settle.
    let attempt = 0;
    const first = deferred<string[]>();
    const second = deferred<string[]>();
    const fetcher = vi.fn(() => {
      attempt += 1;
      return attempt === 1 ? first.promise : second.promise;
    });

    render(
      <AsyncPanel
        fetcher={fetcher}
        empty={<div>nothing</div>}
        errorMessage="Couldn't load."
      >
        {(items) => <div data-testid="data">{items.join(',')}</div>}
      </AsyncPanel>,
    );

    await act(async () => { first.reject(new Error('boom')); });
    expect(await screen.findByText("Couldn't load.")).toBeInTheDocument();

    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await user.click(screen.getByRole('button', { name: 'Retry' }));

    await act(async () => { second.resolve(['fresh']); });
    expect(await screen.findByTestId('data')).toHaveTextContent('fresh');
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('treats a fetcher that never settles as a timeout error', async () => {
    const never = new Promise<string[]>(() => { /* never resolves */ });

    render(
      <AsyncPanel
        fetcher={() => never}
        timeoutMs={200}
        skeletonDelayMs={10_000}  // suppress skeleton so we observe the error directly
        empty={<div>nothing</div>}
        errorMessage={(err) => err.name === 'TimeoutError' ? 'It took too long.' : 'Other error.'}
      >
        {() => <div>data</div>}
      </AsyncPanel>,
    );

    await act(async () => { vi.advanceTimersByTime(250); });

    expect(await screen.findByText('It took too long.')).toBeInTheDocument();
  });
});
