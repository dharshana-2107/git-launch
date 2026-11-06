import { useNavigate } from "react-router-dom";
import { api } from "../api";
import ProjectForm from "../components/ProjectForm";

export default function NewProjectPage() {
  const navigate = useNavigate();

  return (
    <>
      <h1>New project</h1>
      <ProjectForm
        submitLabel="Create project"
        onSubmit={async (input) => {
          const project = await api.createProject(input);
          navigate(`/projects/${project.id}`); // go to the new project's page
        }}
      />
    </>
  );
}
