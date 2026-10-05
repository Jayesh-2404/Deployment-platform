import type { ComponentChildren, JSX } from "preact";
import type { DeploymentStatus } from "./api";

/** Badge describing a deployment's current status. */
export function StatusBadge({ status }: { status: DeploymentStatus }) {
  return <span class={`badge badge--${status}`}>{label(status)}</span>;
}

function label(status: DeploymentStatus): string {
  switch (status) {
    case "running_health_check":
      return "health check";
    case "rolled_back":
      return "rolled back";
    default:
      return status;
  }
}

/** Panel is the standard surface used for every region of the dashboard. */
export function Panel(props: {
  title: string;
  meta?: string;
  actions?: ComponentChildren;
  children: ComponentChildren;
}) {
  return (
    <section class="panel">
      <header class="panel__header">
        <div>
          <h2>{props.title}</h2>
          {props.meta ? <p class="panel__meta">{props.meta}</p> : null}
        </div>
        {props.actions ? <div class="panel__actions">{props.actions}</div> : null}
      </header>
      {props.children}
    </section>
  );
}

/** EmptyState replaces a list body when there is nothing to show yet. */
export function EmptyState({ title, hint }: { title: string; hint: string }) {
  return (
    <div class="empty">
      <p class="empty__title">{title}</p>
      <p class="empty__hint">{hint}</p>
    </div>
  );
}

/** Banner surfaces a failure without stealing focus from the page. */
export function ErrorBanner({ message, onDismiss }: { message: string; onDismiss: () => void }) {
  return (
    <div class="banner banner--error" role="alert">
      <span>{message}</span>
      <button type="button" class="banner__close" onClick={onDismiss} aria-label="Dismiss">
        x
      </button>
    </div>
  );
}

// ButtonHTMLAttributes is the Preact type that carries `type`, which
// HTMLAttributes omits -- and `type` matters on a button inside a form.
type ButtonProps = JSX.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "danger";
};

export function Button(props: ButtonProps) {
  const { variant = "secondary", class: className, ...rest } = props;
  // A caller-supplied class is appended so custom styling composes.
  const classes = className ? `btn btn--${variant} ${className}` : `btn btn--${variant}`;
  return <button type="button" {...rest} class={classes} />;
}

/** Duration between two ISO timestamps, rendered compactly. */
export function duration(startedAt: string, finishedAt?: string): string {
  const start = Date.parse(startedAt);
  const end = finishedAt ? Date.parse(finishedAt) : Date.now();
  if (Number.isNaN(start) || Number.isNaN(end)) {
    return "--";
  }
  const seconds = Math.max(0, Math.round((end - start) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** Shorten a commit sha for display without losing its identity. */
export function shortSha(sha: string): string {
  if (!sha) return "-";
  return sha.length > 12 ? sha.slice(0, 12) : sha;
}
