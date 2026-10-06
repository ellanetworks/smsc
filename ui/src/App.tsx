import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import Cores from "@/pages/Cores";
import Messages from "@/pages/Messages";
import Operator from "@/pages/Operator";

export default function App() {
  return (
    <Layout>
      <Routes>
        <Route path="/operator" element={<Operator />} />
        <Route path="/cores" element={<Cores />} />
        <Route path="/messages" element={<Messages />} />
        <Route path="*" element={<Navigate to="/cores" replace />} />
      </Routes>
    </Layout>
  );
}
