export function BrandIcon({
  className = "h-8 w-8",
}: {
  className?: string;
}): React.JSX.Element {
  return (
    <img
      src="/mishmesh-apricot-v1.svg"
      width={32}
      height={32}
      className={className}
      alt=""
      aria-hidden="true"
    />
  );
}
