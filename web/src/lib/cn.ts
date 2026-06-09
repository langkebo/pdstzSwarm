// Tiny className merger. We expose it under lib/ so server components
// can import it without pulling in client-only dependencies.
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
