export const KES_PER_USD = 100;

export function currencySymbol(currency: string) {
  if (currency === "KES") return "Ksh";
  if (currency === "USD") return "$";
  try {
    return (
      new Intl.NumberFormat(undefined, {
        style: "currency",
        currency,
        currencyDisplay: "narrowSymbol",
      })
        .formatToParts(0)
        .find((part) => part.type === "currency")?.value ?? currency
    );
  } catch {
    return currency;
  }
}

export function formatMinor(currency: string, minor: number) {
  const symbol = currencySymbol(currency);
  const separator = /[a-z]$/i.test(symbol) ? " " : "";
  return `${symbol}${separator}${(minor / 100).toLocaleString(undefined, { maximumFractionDigits: 2 })}`;
}

export function formatUSD(kesMinor: number) {
  return formatMinor("USD", Math.round(kesMinor / KES_PER_USD));
}
