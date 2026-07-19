import {
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  forwardRef,
} from "react";
import "./ui.css";

export function Button({
  variant = "primary",
  size = "md",
  loading,
  children,
  disabled,
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "sm" | "md" | "lg";
  loading?: boolean;
}) {
  return (
    <button
      className={`btn btn--${variant} btn--${size} ${className}`}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {loading && <span className="spinner" aria-hidden />}
      <span>{children}</span>
    </button>
  );
}

export const Input = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string; error?: string }
>(function Input({ label, hint, error, id, className = "", required, ...props }, ref) {
  const inputId = id || label.toLowerCase().replace(/\s+/g, "-");
  return (
    <div className={`field ${className}`}>
      <label className="field__label" htmlFor={inputId}>
        {label}
        {required && (
          <span className="field__req" aria-hidden>
            *
          </span>
        )}
      </label>
      <input
        ref={ref}
        id={inputId}
        className={`field__input ${error ? "field__input--error" : ""}`}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${inputId}-err` : hint ? `${inputId}-hint` : undefined}
        required={required}
        {...props}
      />
      {hint && !error && (
        <p className="field__hint" id={`${inputId}-hint`}>
          {hint}
        </p>
      )}
      {error && (
        <p className="field__error" id={`${inputId}-err`} role="alert">
          {error}
        </p>
      )}
    </div>
  );
});

export function Select({
  label,
  id,
  children,
  className = "",
  ...props
}: SelectHTMLAttributes<HTMLSelectElement> & { label: string }) {
  const selectId = id || label.toLowerCase().replace(/\s+/g, "-");
  return (
    <div className={`field ${className}`}>
      <label className="field__label" htmlFor={selectId}>
        {label}
      </label>
      <select id={selectId} className="field__input field__select" {...props}>
        {children}
      </select>
    </div>
  );
}

export function Card({
  children,
  className = "",
  as: Tag = "div",
}: {
  children: ReactNode;
  className?: string;
  as?: "div" | "section" | "article";
}) {
  return <Tag className={`card ${className}`}>{children}</Tag>;
}

export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
}: {
  // eyebrow names the page's nav group (Find / Understand / Review).
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div className="page-header__text">
        {eyebrow && <p className="eyebrow">{eyebrow}</p>}
        <h1>{title}</h1>
        {description && <p className="page-header__desc">{description}</p>}
      </div>
      {actions && <div className="page-header__actions">{actions}</div>}
    </header>
  );
}

// Segmented shows every mode as a visible, clickable option. Labels are
// plain language; the optional hint carries the technical term, shown on
// hover.
export function Segmented<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { value: T; label: string; hint?: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          className={`segmented__opt ${o.value === value ? "is-active" : ""}`}
          aria-pressed={o.value === value}
          title={o.hint}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: "neutral" | "brand" | "success" | "warning" | "danger" | "info";
}) {
  return <span className={`badge badge--${tone}`}>{children}</span>;
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty" role="status">
      <div className="empty__icon" aria-hidden>
        ⌕
      </div>
      <h2 className="empty__title">{title}</h2>
      <p className="empty__desc">{description}</p>
      {action && <div className="empty__action">{action}</div>}
    </div>
  );
}

export function Skeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="skeleton-block" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="skeleton-line" style={{ width: `${88 - i * 8}%` }} />
      ))}
    </div>
  );
}

export function Alert({
  tone = "info",
  title,
  children,
  onDismiss,
}: {
  tone?: "info" | "success" | "warning" | "danger";
  title?: string;
  children: ReactNode;
  onDismiss?: () => void;
}) {
  return (
    <div className={`alert alert--${tone}`} role={tone === "danger" ? "alert" : "status"}>
      <div className="alert__body">
        {title && <strong className="alert__title">{title}</strong>}
        <div className="alert__content">{children}</div>
      </div>
      {onDismiss && (
        <button type="button" className="alert__close" onClick={onDismiss} aria-label="Dismiss">
          ×
        </button>
      )}
    </div>
  );
}

export function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="stat">
      <div className="stat__value">{value}</div>
      <div className="stat__label">{label}</div>
    </div>
  );
}
