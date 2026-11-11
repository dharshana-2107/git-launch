import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter, Link, NavLink, Route, Routes } from "react-router-dom";
import "@fontsource-variable/bricolage-grotesque";
import "@fontsource-variable/manrope";
import "@fontsource-variable/jetbrains-mono";
import ProjectsPage from "./pages/ProjectsPage";
import NewProjectPage from "./pages/NewProjectPage";
import ProjectPage from "./pages/ProjectPage";
import Logo from "./components/Logo";
import "./styles.css";

// The router maps each URL to a page component. Navigating between pages
// happens in the browser without a full page reload (a "single-page app").
function App() {
  return (
    <BrowserRouter>
      <header className="topbar">
        <div className="topbar-inner">
          <Link to="/" className="brand">
            <Logo />
            <span>GitLaunch</span>
          </Link>
          <nav className="topnav">
            <NavLink to="/" end>
              Projects
            </NavLink>
            <a href="http://localhost:8090" target="_blank" rel="noreferrer">
              Traefik
            </a>
          </nav>
        </div>
      </header>
      <main className="container">
        <Routes>
          <Route path="/" element={<ProjectsPage />} />
          <Route path="/projects/new" element={<NewProjectPage />} />
          <Route path="/projects/:id" element={<ProjectPage />} />
          <Route
            path="*"
            element={
              <div className="empty">
                <h1>Page not found</h1>
                <Link to="/" className="button primary">
                  Go to projects
                </Link>
              </div>
            }
          />
        </Routes>
      </main>
    </BrowserRouter>
  );
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
