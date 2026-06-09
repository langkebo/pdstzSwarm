import { AuthGuard } from "@/components/AuthGuard";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { Sidebar } from "@/components/Sidebar";

/**
 * (app) group layout — wraps every route EXCEPT /login with the
 * persistent left rail + scrolling main pane. Next.js's route group
 * syntax `()` means this segment is part of the URL path.
 *
 * AuthGuard ensures the user is authenticated before rendering
 * any app route. Unauthenticated users are redirected to /login.
 */
export default function AppLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <AuthGuard>
      <ErrorBoundary>
        <div className="flex h-screen overflow-hidden">
          <Sidebar />
          <main className="flex-1 overflow-auto bg-background">
            {children}
          </main>
        </div>
      </ErrorBoundary>
    </AuthGuard>
  );
}
