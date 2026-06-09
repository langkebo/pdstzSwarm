"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { fetchUser, setCachedUser } from "@/lib/auth-cache";

type AuthState = "loading" | "authenticated" | "unauthenticated";

export function AuthGuard({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<AuthState>("loading");
  const router = useRouter();
  const pathname = usePathname();

  useEffect(() => {
    let cancelled = false;
    fetchUser()
      .then((user) => {
        if (!cancelled) {
          setCachedUser(user);
          setState(user ? "authenticated" : "unauthenticated");
        }
      })
      .catch(() => {
        if (!cancelled) setState("unauthenticated");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (state === "unauthenticated") {
      const redirect = encodeURIComponent(pathname);
      router.replace(`/login?redirect=${redirect}`);
    }
  }, [state, pathname, router]);

  if (state === "loading") {
    return (
      <div className="flex h-screen items-center justify-center bg-black">
        <div className="flex flex-col items-center gap-4">
          <div className="h-8 w-8 animate-spin rounded-full border-2 border-emerald-500 border-t-transparent" />
          <span className="text-sm text-zinc-500">Loading...</span>
        </div>
      </div>
    );
  }

  if (state === "unauthenticated") {
    return null;
  }

  return <>{children}</>;
}