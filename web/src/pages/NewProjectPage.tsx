import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import ProjectForm from "../components/ProjectForm";

export default function NewProjectPage() {
  const navigate = useNavigate();

  return (
    <div className="form-page">
      <nav className="crumbs">
        <Link to="/">Projects</Link>
        <span aria-hidden="true">/</span>
        <span>New project</span>
      </nav>
      <h1>New project</h1>
      <p className="lede">Point Launchpad at a public GitHub repo with a Dockerfile. You can deploy it right after.</p>
      <ProjectForm
        submitLabel="Create project"
        onSubmit={async (input) => {
          const project = await api.createProject(input);
          navigate(`/projects/${project.id}`); // go to the new project's page
        }}
      />
    </div>
  );
}
