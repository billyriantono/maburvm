// A flat country flag from lipis/flag-icons, addressed by ISO 3166-1 alpha-2.
//
// Deliberately this library rather than emoji: emoji flags simply do not render
// on Windows (the browser falls back to the two letters), so half of a customer
// base would see no flag at all. The library ships a consistent flat set that
// looks the same everywhere.
//
// The CSS is imported once in globals.css, so this component only picks a class.
export function CountryFlag({
  country,
  className = "",
  title,
}: {
  country?: string
  className?: string
  title?: string
}) {
  const code = (country ?? "").trim().toLowerCase()
  // Two letters or nothing: an unknown code would otherwise render as an empty
  // box, which reads as a broken image rather than as missing data.
  if (code.length !== 2) return null
  return (
    <span
      className={`fi fi-${code} rounded-[2px] ${className}`}
      title={title ?? country?.toUpperCase()}
      role="img"
      aria-label={title ?? country?.toUpperCase()}
    />
  )
}
