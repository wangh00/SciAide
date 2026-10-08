import { type ReactNode, type ComponentPropsWithoutRef } from 'react';

type Tone = 'neutral' | 'pending' | 'success' | 'danger';
type FrameProps = ComponentPropsWithoutRef<'section'> & { tone?: Tone };

// Presentation only: execution, confirmation and scientific content belong to callers.
export function ResearchCardFrame({ tone = 'neutral', className = '', ...props }: FrameProps) {
  return <section {...props} className={`research-card research-card--${tone} ${className}`.trim()}/>;
}

export function ResearchCard({ title, eyebrow, description, status, icon, children, footer,
  bodyClassName = '', headingClassName = '', badgeClassName = '', bodyMode = 'padded',
  titleLevel = 3, ...frame }: Omit<FrameProps, 'title'> & {
  title: ReactNode; eyebrow?: ReactNode; description?: ReactNode; status?: ReactNode;
  icon?: ReactNode; footer?: ReactNode; bodyClassName?: string;
  headingClassName?: string; badgeClassName?: string; bodyMode?: 'padded' | 'contents';
  titleLevel?: 2 | 3;
}) {
  const Heading = titleLevel === 2 ? 'h2' : 'h3';
  return <ResearchCardFrame {...frame}>
    <header className={`research-card-heading ${headingClassName}`.trim()}>
      {icon && <span className="research-card-icon" aria-hidden="true">{icon}</span>}
      <div className="research-card-heading-copy">
        {eyebrow && <p className="research-card-eyebrow">{eyebrow}</p>}
        <Heading>{title}</Heading>
        {description && <small className="research-card-description">{description}</small>}
      </div>
      {status && <span className={`research-card-badge ${badgeClassName}`.trim()}>{status}</span>}
    </header>
    {bodyMode === 'contents' ? children : <div className={`research-card-body ${bodyClassName}`.trim()}>{children}</div>}
    {footer && <footer className="research-card-footer">{footer}</footer>}
  </ResearchCardFrame>;
}
